package azurerm

import (
	"bytes"
	"fmt"
	"log"

	"github.com/Azure/azure-sdk-for-go/services/preview/monitor/mgmt/2018-03-01/insights"
	"github.com/hashicorp/terraform/helper/hashcode"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/helper/validation"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/helpers/azure"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/helpers/response"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/utils"
)

func resourceArmMonitorMetricAlert() *schema.Resource {
	return &schema.Resource{
		Create: resourceArmMonitorMetricAlertCreateOrUpdate,
		Read:   resourceArmMonitorMetricAlertRead,
		Update: resourceArmMonitorMetricAlertCreateOrUpdate,
		Delete: resourceArmMonitorMetricAlertDelete,

		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},

		Schema: map[string]*schema.Schema{
			"name": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.NoZeroValues,
			},

			"resource_group_name": resourceGroupNameSchema(),

			// TODO: Multiple resource IDs (Remove MaxItems) support is missing in SDK
			// Issue to track: https://github.com/Azure/azure-sdk-for-go/issues/2920
			// But to prevent potential state migration in the future, let's stick to use Set now
			"scopes": {
				Type:     schema.TypeSet,
				Required: true,
				MinItems: 1,
				MaxItems: 1,
				Elem: &schema.Schema{
					Type:         schema.TypeString,
					ValidateFunc: azure.ValidateResourceID,
				},
				Set: schema.HashString,
			},

			"criteria": {
				Type:     schema.TypeList,
				Required: true,
				MinItems: 1,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"metric_namespace": {
							Type:         schema.TypeString,
							Required:     true,
							ValidateFunc: validation.NoZeroValues,
						},
						"metric_name": {
							Type:         schema.TypeString,
							Required:     true,
							ValidateFunc: validation.NoZeroValues,
						},
						"aggregation": {
							Type:     schema.TypeString,
							Required: true,
							ValidateFunc: validation.StringInSlice([]string{
								"Average",
								"Minimum",
								"Maximum",
								"Total",
							}, true),
							DiffSuppressFunc: ignoreCaseDiffSuppressFunc,
						},
						"operator": {
							Type:     schema.TypeString,
							Required: true,
							ValidateFunc: validation.StringInSlice([]string{
								"Equals",
								"NotEquals",
								"GreaterThan",
								"GreaterThanOrEqual",
								"LessThan",
								"LessThanOrEqual",
							}, true),
							DiffSuppressFunc: ignoreCaseDiffSuppressFunc,
						},
						"threshold": {
							Type:     schema.TypeFloat,
							Required: true,
						},
						"dimension": {
							Type:     schema.TypeList,
							Optional: true,
							Elem: &schema.Resource{
								Schema: map[string]*schema.Schema{
									"name": {
										Type:         schema.TypeString,
										Required:     true,
										ValidateFunc: validation.NoZeroValues,
									},
									"values": {
										Type:     schema.TypeList,
										Required: true,
										MinItems: 1,
										Elem: &schema.Schema{
											Type: schema.TypeString,
										},
									},
									"operator": {
										Type:     schema.TypeString,
										Optional: true,
									},
								},
							},
						},
					},
				},
			},

			"action": {
				Type:     schema.TypeSet,
				Optional: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"action_group_id": {
							Type:         schema.TypeString,
							Required:     true,
							ValidateFunc: azure.ValidateResourceID,
						},
						"webhook_properties": {
							Type:     schema.TypeMap,
							Optional: true,
							Elem: &schema.Schema{
								Type: schema.TypeString,
							},
						},
					},
				},
				Set: resourceArmMonitorMetricAlertActionHash,
			},

			"auto_mitigate": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},

			"description": {
				Type:     schema.TypeString,
				Optional: true,
			},

			"enabled": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  true,
			},

			"frequency": {
				Type:     schema.TypeString,
				Optional: true,
				Default:  "PT1M",
				ValidateFunc: validation.StringInSlice([]string{
					"PT1M",
					"PT5M",
					"PT15M",
					"PT30M",
					"PT1H",
				}, false),
			},

			"severity": {
				Type:         schema.TypeInt,
				Optional:     true,
				Default:      3,
				ValidateFunc: validation.IntBetween(0, 4),
			},

			"window_size": {
				Type:     schema.TypeString,
				Optional: true,
				Default:  "PT5M",
				ValidateFunc: validation.StringInSlice([]string{
					"PT1M",
					"PT5M",
					"PT15M",
					"PT30M",
					"PT1H",
					"PT6H",
					"PT12H",
					"P1D",
				}, false),
			},

			"tags": tagsSchema(),
		},
	}
}

func resourceArmMonitorMetricAlertCreateOrUpdate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient).monitorMetricAlertsClient
	ctx := meta.(*ArmClient).StopContext

	name := d.Get("name").(string)
	resGroup := d.Get("resource_group_name").(string)

	enabled := d.Get("enabled").(bool)
	autoMitigate := d.Get("auto_mitigate").(bool)
	description := d.Get("description").(string)
	scopesRaw := d.Get("scopes").(*schema.Set).List()
	severity := d.Get("severity").(int)
	frequency := d.Get("frequency").(string)
	windowSize := d.Get("window_size").(string)
	criteriaRaw := d.Get("criteria").([]interface{})
	actionRaw := d.Get("action").(*schema.Set).List()

	tags := d.Get("tags").(map[string]interface{})
	expandedTags := expandTags(tags)

	parameters := insights.MetricAlertResource{
		Location: utils.String(azureRMNormalizeLocation("Global")),
		MetricAlertProperties: &insights.MetricAlertProperties{
			Enabled:             utils.Bool(enabled),
			AutoMitigate:        utils.Bool(autoMitigate),
			Description:         utils.String(description),
			Severity:            utils.Int32(int32(severity)),
			EvaluationFrequency: utils.String(frequency),
			WindowSize:          utils.String(windowSize),
			Scopes:              expandMonitorMetricAlertStringArray(scopesRaw),
			Criteria:            expandMonitorMetricAlertCriteria(criteriaRaw),
			Actions:             expandMonitorMetricAlertAction(actionRaw),
		},
		Tags: expandedTags,
	}

	if _, err := client.CreateOrUpdate(ctx, resGroup, name, parameters); err != nil {
		return fmt.Errorf("Error creating or updating metric alert %q (resource group %q): %+v", name, resGroup, err)
	}

	read, err := client.Get(ctx, resGroup, name)
	if err != nil {
		return err
	}
	if read.ID == nil {
		return fmt.Errorf("Metric alert %q (resource group %q) ID is empty", name, resGroup)
	}
	d.SetId(*read.ID)

	return resourceArmMonitorMetricAlertRead(d, meta)
}

func resourceArmMonitorMetricAlertRead(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient).monitorMetricAlertsClient
	ctx := meta.(*ArmClient).StopContext

	id, err := parseAzureResourceID(d.Id())
	if err != nil {
		return err
	}
	resGroup := id.ResourceGroup
	name := id.Path["metricAlerts"]

	resp, err := client.Get(ctx, resGroup, name)
	if err != nil {
		if utils.ResponseWasNotFound(resp.Response) {
			log.Printf("[DEBUG] Metric Alert %q was not found in Resource Group %q - removing from state!", name, resGroup)
			d.SetId("")
			return nil
		}
		return fmt.Errorf("Error getting metric alert %q (resource group %q): %+v", name, resGroup, err)
	}

	d.Set("name", name)
	d.Set("resource_group_name", resGroup)
	if alert := resp.MetricAlertProperties; alert != nil {
		d.Set("enabled", alert.Enabled)
		d.Set("auto_mitigate", alert.AutoMitigate)
		d.Set("description", alert.Description)
		d.Set("severity", alert.Severity)
		d.Set("frequency", alert.EvaluationFrequency)
		d.Set("window_size", alert.WindowSize)
		if err := d.Set("scopes", flattenMonitorMetricAlertStringArray(alert.Scopes)); err != nil {
			return fmt.Errorf("Error setting `scopes`: %+v", err)
		}
		if err := d.Set("criteria", flattenMonitorMetricAlertCriteria(alert.Criteria)); err != nil {
			return fmt.Errorf("Error setting `criteria`: %+v", err)
		}
		if err := d.Set("action", flattenMonitorMetricAlertAction(alert.Actions)); err != nil {
			return fmt.Errorf("Error setting `action`: %+v", err)
		}
	}
	flattenAndSetTags(d, resp.Tags)

	return nil
}

func resourceArmMonitorMetricAlertDelete(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient).monitorMetricAlertsClient
	ctx := meta.(*ArmClient).StopContext

	id, err := parseAzureResourceID(d.Id())
	if err != nil {
		return err
	}
	resGroup := id.ResourceGroup
	name := id.Path["metricAlerts"]

	if resp, err := client.Delete(ctx, resGroup, name); err != nil {
		if !response.WasNotFound(resp.Response) {
			return fmt.Errorf("Error deleting metric alert %q (resource group %q): %+v", name, resGroup, err)
		}
	}

	return nil
}

func expandMonitorMetricAlertStringArray(input []interface{}) *[]string {
	result := make([]string, 0)
	for _, item := range input {
		result = append(result, item.(string))
	}
	return &result
}

func expandMonitorMetricAlertCriteria(input []interface{}) *insights.MetricAlertSingleResourceMultipleMetricCriteria {
	criterias := make([]insights.MetricCriteria, 0)
	for i, item := range input {
		v := item.(map[string]interface{})

		dimensions := make([]insights.MetricDimension, 0)
		for _, dimension := range v["dimension"].([]interface{}) {
			dVal := dimension.(map[string]interface{})
			dimensions = append(dimensions, insights.MetricDimension{
				Name:     utils.String(dVal["name"].(string)),
				Operator: utils.String(dVal["operator"].(string)),
				Values:   expandMonitorMetricAlertStringArray(dVal["values"].([]interface{})),
			})
		}

		criterias = append(criterias, insights.MetricCriteria{
			Name:            utils.String(fmt.Sprintf("Metric%d", i+1)),
			MetricNamespace: utils.String(v["metric_namespace"].(string)),
			MetricName:      utils.String(v["metric_name"].(string)),
			TimeAggregation: v["aggregation"].(string),
			Operator:        v["operator"].(string),
			Threshold:       utils.Float(v["threshold"].(float64)),
			Dimensions:      &dimensions,
		})
	}
	return &insights.MetricAlertSingleResourceMultipleMetricCriteria{
		AllOf:     &criterias,
		OdataType: insights.OdataTypeMicrosoftAzureMonitorSingleResourceMultipleMetricCriteria,
	}
}

func expandMonitorMetricAlertAction(input []interface{}) *[]insights.MetricAlertAction {
	actions := make([]insights.MetricAlertAction, 0)
	for _, item := range input {
		v := item.(map[string]interface{})

		props := make(map[string]*string)
		if pVal, ok := v["webhook_properties"]; ok {
			for pk, pv := range pVal.(map[string]interface{}) {
				props[pk] = utils.String(pv.(string))
			}
		}

		actions = append(actions, insights.MetricAlertAction{
			ActionGroupID:     utils.String(v["action_group_id"].(string)),
			WebhookProperties: props,
		})
	}
	return &actions
}

func flattenMonitorMetricAlertStringArray(input *[]string) []interface{} {
	result := make([]interface{}, 0)
	if input != nil {
		for _, item := range *input {
			result = append(result, item)
		}
	}
	return result
}

func flattenMonitorMetricAlertCriteria(input insights.BasicMetricAlertCriteria) (result []interface{}) {
	result = make([]interface{}, 0)
	if input == nil {
		return
	}
	criteria, ok := input.AsMetricAlertSingleResourceMultipleMetricCriteria()
	if !ok || criteria == nil || criteria.AllOf == nil {
		return
	}
	for _, metric := range *criteria.AllOf {
		v := make(map[string]interface{})

		if metric.MetricNamespace != nil {
			v["metric_namespace"] = *metric.MetricNamespace
		}
		if metric.MetricName != nil {
			v["metric_name"] = *metric.MetricName
		}
		if aggr, ok := metric.TimeAggregation.(string); ok {
			v["aggregation"] = aggr
		}
		if op, ok := metric.Operator.(string); ok {
			v["operator"] = op
		}
		if metric.Threshold != nil {
			v["threshold"] = *metric.Threshold
		}
		if metric.Dimensions != nil {
			dimResult := make([]map[string]interface{}, 0)
			for _, dimension := range *metric.Dimensions {
				dVal := make(map[string]interface{})
				if dimension.Name != nil {
					dVal["name"] = *dimension.Name
				}
				if dimension.Operator != nil {
					dVal["operator"] = *dimension.Operator
				}
				dVal["values"] = flattenMonitorMetricAlertStringArray(dimension.Values)
				dimResult = append(dimResult, dVal)
			}
			v["dimension"] = dimResult
		}

		result = append(result, v)
	}
	return
}

func flattenMonitorMetricAlertAction(input *[]insights.MetricAlertAction) []interface{} {
	result := make([]interface{}, 0)
	if input != nil {
		for _, action := range *input {
			v := make(map[string]interface{}, 0)

			if action.ActionGroupID != nil {
				v["action_group_id"] = *action.ActionGroupID
			}

			props := make(map[string]string)
			for pk, pv := range action.WebhookProperties {
				if pv != nil {
					props[pk] = *pv
				}
			}
			v["webhook_properties"] = props

			result = append(result, v)
		}
	}
	return result
}

func resourceArmMonitorMetricAlertActionHash(input interface{}) int {
	var buf bytes.Buffer
	if v, ok := input.(map[string]interface{}); ok {
		buf.WriteString(fmt.Sprintf("%s-", v["action_group_id"].(string)))
	}
	return hashcode.String(buf.String())
}
