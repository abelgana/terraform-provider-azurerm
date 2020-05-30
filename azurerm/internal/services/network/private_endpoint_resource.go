package network

import (
	"fmt"
	"log"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/network/mgmt/2020-03-01/network"
	"github.com/hashicorp/go-azure-helpers/response"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/helper/validation"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/helpers/azure"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/helpers/tf"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/internal/clients"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/internal/features"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/internal/tags"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/internal/timeouts"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/utils"
)

func resourceArmPrivateEndpoint() *schema.Resource {
	return &schema.Resource{
		Create: resourceArmPrivateEndpointCreateUpdate,
		Read:   resourceArmPrivateEndpointRead,
		Update: resourceArmPrivateEndpointCreateUpdate,
		Delete: resourceArmPrivateEndpointDelete,
		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(60 * time.Minute),
			Read:   schema.DefaultTimeout(5 * time.Minute),
			Update: schema.DefaultTimeout(60 * time.Minute),
			Delete: schema.DefaultTimeout(60 * time.Minute),
		},

		Schema: map[string]*schema.Schema{
			"name": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: ValidatePrivateLinkName,
			},

			"location": azure.SchemaLocation(),

			"resource_group_name": azure.SchemaResourceGroupNameDiffSuppress(),

			"subnet_id": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: azure.ValidateResourceID,
			},

			"private_dns_zone_group": {
				Type:     schema.TypeList,
				Optional: true,
				MaxItems: 1,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": {
							Type:         schema.TypeString,
							Required:     true,
							ValidateFunc: ValidatePrivateLinkName,
						},
						"id": {
							Type:     schema.TypeString,
							Computed: true,
						},
						"zone_config": {
							Type:     schema.TypeList,
							Required: true,
							Elem: &schema.Resource{
								Schema: map[string]*schema.Schema{
									"name": {
										Type:     schema.TypeString,
										Required: true,
									},
									"private_dns_zone_id": {
										Type:     schema.TypeString,
										Required: true,
									},
								},
							},
						},
					},
				},
			},

			"private_service_connection": {
				Type:     schema.TypeList,
				Required: true,
				MaxItems: 1,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": {
							Type:         schema.TypeString,
							Required:     true,
							ForceNew:     true,
							ValidateFunc: ValidatePrivateLinkName,
						},
						"is_manual_connection": {
							Type:     schema.TypeBool,
							Required: true,
							ForceNew: true,
						},
						"private_connection_resource_id": {
							Type:         schema.TypeString,
							Required:     true,
							ForceNew:     true,
							ValidateFunc: azure.ValidateResourceID,
						},
						"subresource_names": {
							Type:     schema.TypeList,
							Optional: true,
							ForceNew: true,
							Elem: &schema.Schema{
								Type:         schema.TypeString,
								ValidateFunc: ValidatePrivateLinkSubResourceName,
							},
						},
						"request_message": {
							Type:         schema.TypeString,
							Optional:     true,
							ValidateFunc: validation.StringLenBetween(1, 140),
						},
						"private_ip_address": {
							Type:     schema.TypeString,
							Computed: true,
						},
					},
				},
			},

			"custom_dns_configs": {
				Type:     schema.TypeList,
				Computed: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"fqdn": {
							Type:     schema.TypeString,
							Computed: true,
						},
						"ip_addresses": {
							Type:     schema.TypeList,
							Computed: true,
							Elem: &schema.Schema{
								Type: schema.TypeString,
							},
						},
					},
				},
			},

			"tags": tags.Schema(),
		},
	}
}

func resourceArmPrivateEndpointCreateUpdate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).Network.PrivateEndpointClient
	ctx, cancel := timeouts.ForCreateUpdate(meta.(*clients.Client).StopContext, d)
	defer cancel()

	name := d.Get("name").(string)
	resourceGroup := d.Get("resource_group_name").(string)

	if err := ValidatePrivateEndpointSettings(d); err != nil {
		return fmt.Errorf("validating the configuration for the Private Endpoint %q (Resource Group %q): %+v", name, resourceGroup, err)
	}

	if features.ShouldResourcesBeImported() && d.IsNewResource() {
		existing, err := client.Get(ctx, resourceGroup, name, "")
		if err != nil {
			if !utils.ResponseWasNotFound(existing.Response) {
				return fmt.Errorf("checking for presence of existing Private Endpoint %q (Resource Group %q): %+v", name, resourceGroup, err)
			}
		}

		if existing.ID != nil && *existing.ID != "" {
			return tf.ImportAsExistsError("azurerm_private_endpoint", *existing.ID)
		}
	}

	location := azure.NormalizeLocation(d.Get("location").(string))
	privateDnsZoneGroup := d.Get("private_dns_zone_group").([]interface{})
	privateServiceConnections := d.Get("private_service_connection").([]interface{})
	subnetId := d.Get("subnet_id").(string)

	parameters := network.PrivateEndpoint{
		Location: utils.String(location),
		PrivateEndpointProperties: &network.PrivateEndpointProperties{
			PrivateLinkServiceConnections:       expandArmPrivateLinkEndpointServiceConnection(privateServiceConnections, false),
			ManualPrivateLinkServiceConnections: expandArmPrivateLinkEndpointServiceConnection(privateServiceConnections, true),
			Subnet: &network.Subnet{
				ID: utils.String(subnetId),
			},
		},
		Tags: tags.Expand(d.Get("tags").(map[string]interface{})),
	}

	future, err := client.CreateOrUpdate(ctx, resourceGroup, name, parameters)
	if err != nil {
		if azure.StringContains(err.Error(), "is missing required parameter 'group Id'") {
			return fmt.Errorf("creating Private Endpoint %q (Resource Group %q) due to missing 'group Id', ensure that the 'subresource_names' type is populated: %+v", name, resourceGroup, err)
		} else {
			return fmt.Errorf("creating Private Endpoint %q (Resource Group %q): %+v", name, resourceGroup, err)
		}
	}
	if err = future.WaitForCompletionRef(ctx, client.Client); err != nil {
		return fmt.Errorf("waiting for creation of Private Endpoint %q (Resource Group %q): %+v", name, resourceGroup, err)
	}

	dnsGroupParameters := expandArmPrivateDnsZoneGroup(privateDnsZoneGroup)

	if dnsGroupName := dnsGroupParameters.Name; dnsGroupName != nil {
		dnsClient := meta.(*clients.Client).Network.PrivateDnsZoneGroupClient
		future, err := dnsClient.CreateOrUpdate(ctx, resourceGroup, name, *dnsGroupName, dnsGroupParameters)
		if err != nil {
			return fmt.Errorf("creating Private Endpoint DNS Zone Group %q (Resource Group %q): %+v", dnsGroupName, resourceGroup, err)
		}
		if err = future.WaitForCompletionRef(ctx, dnsClient.Client); err != nil {
			return fmt.Errorf("waiting for creation of Private Endpoint DNS Zone Group %q (Resource Group %q): %+v", dnsGroupName, resourceGroup, err)
		}
	}

	resp, err := client.Get(ctx, resourceGroup, name, "")
	if err != nil {
		return fmt.Errorf("retrieving Private Endpoint %q (Resource Group %q): %+v", name, resourceGroup, err)
	}
	if resp.ID == nil || *resp.ID == "" {
		return fmt.Errorf("API returns a nil/empty id on Private Endpoint %q (Resource Group %q): %+v", name, resourceGroup, err)
	}
	d.SetId(*resp.ID)

	return resourceArmPrivateEndpointRead(d, meta)
}

func resourceArmPrivateEndpointRead(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).Network.PrivateEndpointClient
	nicsClient := meta.(*clients.Client).Network.InterfacesClient
	dnsClient := meta.(*clients.Client).Network.PrivateDnsZoneGroupClient
	ctx, cancel := timeouts.ForRead(meta.(*clients.Client).StopContext, d)
	defer cancel()

	id, err := azure.ParseAzureResourceID(d.Id())
	if err != nil {
		return err
	}
	resourceGroup := id.ResourceGroup
	name := id.Path["privateEndpoints"]

	resp, err := client.Get(ctx, resourceGroup, name, "")
	if err != nil {
		if utils.ResponseWasNotFound(resp.Response) {
			log.Printf("[INFO] Private Endpoint %q does not exist - removing from state", d.Id())
			d.SetId("")
			return nil
		}
		return fmt.Errorf("reading Private Endpoint %q (Resource Group %q): %+v", name, resourceGroup, err)
	}

	d.Set("name", resp.Name)
	d.Set("resource_group_name", resourceGroup)
	if location := resp.Location; location != nil {
		d.Set("location", azure.NormalizeLocation(*location))
	}

	if props := resp.PrivateEndpointProperties; props != nil {
		privateIpAddress := ""

		if nics := props.NetworkInterfaces; nics != nil && len(*nics) > 0 {
			nic := (*nics)[0]
			if nic.ID != nil && *nic.ID != "" {
				privateIpAddress = getPrivateIpAddress(ctx, nicsClient, *nic.ID)
			}
		}

		flattenedConnection := flattenArmPrivateLinkEndpointServiceConnection(props.PrivateLinkServiceConnections, props.ManualPrivateLinkServiceConnections)
		for _, item := range flattenedConnection {
			v := item.(map[string]interface{})
			v["private_ip_address"] = privateIpAddress
		}
		if err := d.Set("private_service_connection", flattenedConnection); err != nil {
			return fmt.Errorf("setting `private_service_connection`: %+v", err)
		}

		dnsGroupParameters := expandArmPrivateDnsZoneGroup(d.Get("private_dns_zone_group").([]interface{}))

		if zoneGroupName := dnsGroupParameters.Name; zoneGroupName != nil {
			dnsResp, err := dnsClient.Get(ctx, resourceGroup, name, *zoneGroupName)
			if err != nil {
				return fmt.Errorf("reading Private Endpoint %q DNS Zone Group %q (Resource Group %q): %+v", name, zoneGroupName, resourceGroup, err)
			}
			d.Set("private_dns_zone_group", flattenArmPrivateDnsZoneGroup(&dnsResp))
		}

		subnetId := ""
		if subnet := props.Subnet; subnet != nil {
			subnetId = *subnet.ID
		}
		d.Set("subnet_id", subnetId)
		foo := flattenArmCustomDnsConfigs(props.CustomDNSConfigs)
		d.Set("custom_dns_configs", foo)
		log.Printf("\n\n\n\n\n\n\n********************************\nflattenArmCustomDnsConfigs == %+v\nlen == %d\n********************************\n\n\n\n\n\n\n\n\n\n\n\n", foo, len(*props.CustomDNSConfigs))
	}

	return tags.FlattenAndSet(d, resp.Tags)
}

func resourceArmPrivateEndpointDelete(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).Network.PrivateEndpointClient
	ctx, cancel := timeouts.ForDelete(meta.(*clients.Client).StopContext, d)
	defer cancel()

	id, err := azure.ParseAzureResourceID(d.Id())
	if err != nil {
		return err
	}
	resourceGroup := id.ResourceGroup
	name := id.Path["privateEndpoints"]

	future, err := client.Delete(ctx, resourceGroup, name)
	if err != nil {
		if response.WasNotFound(future.Response()) {
			return nil
		}
		return fmt.Errorf("deleting Private Endpoint %q (Resource Group %q): %+v", name, resourceGroup, err)
	}

	if err = future.WaitForCompletionRef(ctx, client.Client); err != nil {
		if !response.WasNotFound(future.Response()) {
			return fmt.Errorf("waiting for deletion of Private Endpoint %q (Resource Group %q): %+v", name, resourceGroup, err)
		}
	}

	return nil
}

func expandArmPrivateLinkEndpointServiceConnection(input []interface{}, parseManual bool) *[]network.PrivateLinkServiceConnection {
	results := make([]network.PrivateLinkServiceConnection, 0)

	for _, item := range input {
		v := item.(map[string]interface{})
		privateConnectonResourceId := v["private_connection_resource_id"].(string)
		subresourceNames := v["subresource_names"].([]interface{})
		requestMessage := v["request_message"].(string)
		isManual := v["is_manual_connection"].(bool)
		name := v["name"].(string)

		if isManual == parseManual {
			result := network.PrivateLinkServiceConnection{
				Name: utils.String(name),
				PrivateLinkServiceConnectionProperties: &network.PrivateLinkServiceConnectionProperties{
					GroupIds:             utils.ExpandStringSlice(subresourceNames),
					PrivateLinkServiceID: utils.String(privateConnectonResourceId),
				},
			}

			if requestMessage != "" {
				result.PrivateLinkServiceConnectionProperties.RequestMessage = utils.String(requestMessage)
			}

			results = append(results, result)
		}
	}

	return &results
}

func expandArmPrivateDnsZoneGroup(input []interface{}) network.PrivateDNSZoneGroup {
	result := network.PrivateDNSZoneGroup{}
	if len(input) == 0 {
		return result
	}

	dnsZoneConfigs := make([]network.PrivateDNSZoneConfig, 0)

	for _, item := range input {
		v := item.(map[string]interface{})
		name := v["name"].(string)
		zoneConfigs := v["zone_config"].([]interface{})

		result.Name = utils.String(name)

		for _, zoneConfig := range zoneConfigs {
			z := zoneConfig.(map[string]interface{})
			zoneName := z["name"].(string)
			zoneId := z["private_dns_zone_id"].(string)

			config := network.PrivateDNSZoneConfig{
				Name: utils.String(zoneName),
				PrivateDNSZonePropertiesFormat: &network.PrivateDNSZonePropertiesFormat{
					PrivateDNSZoneID: utils.String(zoneId),
				},
			}

			dnsZoneConfigs = append(dnsZoneConfigs, config)
		}
	}

	result.PrivateDNSZoneGroupPropertiesFormat = &network.PrivateDNSZoneGroupPropertiesFormat{
		PrivateDNSZoneConfigs: &dnsZoneConfigs,
	}

	return result
}

func flattenArmPrivateDnsZoneGroup(customDnsGroup *network.PrivateDNSZoneGroup) []interface{} {
	results := make([]interface{}, 0)
	if customDnsGroup == nil {
		return results
	}

	if props := customDnsGroup.PrivateDNSZoneGroupPropertiesFormat; props != nil {
		v := flattenArmPrivateDnsZoneConfigs(props.PrivateDNSZoneConfigs)

		results = append(results, map[string]interface{}{
			"name":        customDnsGroup.Name,
			"id":          customDnsGroup.ID,
			"zone_config": v,
		})
	}

	return results
}

func flattenArmPrivateDnsZoneConfigs(input *[]network.PrivateDNSZoneConfig) []interface{} {
	output := make([]interface{}, 0)
	if input == nil {
		return output
	}
	log.Printf("\n\n\n\n\n\n\n********************************\n")
	for _, v := range *input {
		result := make(map[string]interface{})

		if name := v.Name; name != nil {
			result["name"] = *name
		}
		if zoneId := v.PrivateDNSZonePropertiesFormat.PrivateDNSZoneID; zoneId != nil {
			result["private_dns_zone_id"] = *zoneId
		}

		log.Printf("\nRS LEN  == %d\n", len(*v.PrivateDNSZonePropertiesFormat.RecordSets))

		for _, rs := range *v.PrivateDNSZonePropertiesFormat.RecordSets {
			log.Printf("\nFQDN  == %s\nIPAddresses == %+v\n\n", *rs.Fqdn, rs.IPAddresses)
		}

		output = append(output, result)
	}
	log.Printf("********************************\n\n\n\n\n\n\n\n\n\n\n\n")
	return output
}

func flattenArmCustomDnsConfigs(customDnsConfigs *[]network.CustomDNSConfigPropertiesFormat) []interface{} {
	results := make([]interface{}, 0)
	if customDnsConfigs == nil {
		return results
	}

	for _, item := range *customDnsConfigs {
		results = append(results, map[string]interface{}{
			"fqdn":         item.Fqdn,
			"ip_addresses": utils.FlattenStringSlice(item.IPAddresses),
		})
	}

	return results
}

func flattenArmPrivateLinkEndpointServiceConnection(serviceConnections *[]network.PrivateLinkServiceConnection, manualServiceConnections *[]network.PrivateLinkServiceConnection) []interface{} {
	results := make([]interface{}, 0)
	if serviceConnections == nil && manualServiceConnections == nil {
		return results
	}

	if serviceConnections != nil {
		for _, item := range *serviceConnections {
			name := ""
			if item.Name != nil {
				name = *item.Name
			}

			privateConnectionId := ""
			subResourceNames := make([]interface{}, 0)

			if props := item.PrivateLinkServiceConnectionProperties; props != nil {
				if v := props.GroupIds; v != nil {
					subResourceNames = utils.FlattenStringSlice(v)
				}
				if props.PrivateLinkServiceID != nil {
					privateConnectionId = *props.PrivateLinkServiceID
				}
			}
			results = append(results, map[string]interface{}{
				"name":                           name,
				"is_manual_connection":           false,
				"private_connection_resource_id": privateConnectionId,
				"subresource_names":              subResourceNames,
			})
		}
	}

	if manualServiceConnections != nil {
		for _, item := range *manualServiceConnections {
			name := ""
			if item.Name != nil {
				name = *item.Name
			}

			privateConnectionId := ""
			requestMessage := ""
			subResourceNames := make([]interface{}, 0)

			if props := item.PrivateLinkServiceConnectionProperties; props != nil {
				if v := props.GroupIds; v != nil {
					subResourceNames = utils.FlattenStringSlice(v)
				}
				if props.PrivateLinkServiceID != nil {
					privateConnectionId = *props.PrivateLinkServiceID
				}
				if props.RequestMessage != nil {
					requestMessage = *props.RequestMessage
				}
			}

			results = append(results, map[string]interface{}{
				"name":                           name,
				"is_manual_connection":           true,
				"private_connection_resource_id": privateConnectionId,
				"request_message":                requestMessage,
				"subresource_names":              subResourceNames,
			})
		}
	}

	return results
}
