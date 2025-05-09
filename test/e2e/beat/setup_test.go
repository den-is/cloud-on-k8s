// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License 2.0;
// you may not use this file except in compliance with the Elastic License 2.0.

//go:build beat || e2e

package beat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	esv1 "github.com/elastic/cloud-on-k8s/v3/pkg/apis/elasticsearch/v1"
	"github.com/elastic/cloud-on-k8s/v3/pkg/controller/beat/filebeat"
	"github.com/elastic/cloud-on-k8s/v3/pkg/controller/beat/heartbeat"
	"github.com/elastic/cloud-on-k8s/v3/pkg/controller/beat/metricbeat"
	"github.com/elastic/cloud-on-k8s/v3/pkg/controller/common/version"
	"github.com/elastic/cloud-on-k8s/v3/test/e2e/test"
	"github.com/elastic/cloud-on-k8s/v3/test/e2e/test/beat"
	"github.com/elastic/cloud-on-k8s/v3/test/e2e/test/elasticsearch"
	"github.com/elastic/cloud-on-k8s/v3/test/e2e/test/kibana"
)

type kbSavedObjects struct {
	Total        int `json:"total"`
	SavedObjects []struct {
		Attributes struct {
			Title string `json:"title"`
		} `json:"attributes"`
	} `json:"saved_objects"`
}

func TestBeatKibanaRefWithTLSDisabled(t *testing.T) {
	name := "test-beat-kibanaref-no-tls"

	esBuilder := elasticsearch.NewBuilder(name).
		WithESMasterDataNodes(3, elasticsearch.DefaultResources).
		WithTLSDisabled(true)

	kbBuilder := kibana.NewBuilder(name).
		WithNodeCount(1).
		WithElasticsearchRef(esBuilder.Ref()).
		WithTLSDisabled(true)

	fbBuilder := beat.NewBuilder(name).
		WithType(filebeat.Type).
		WithRoles(beat.AutodiscoverClusterRoleName).
		WithOpenShiftRoles(test.UseSCCRole).
		WithElasticsearchRef(esBuilder.Ref()).
		WithKibanaRef(kbBuilder.Ref())

	fileBeatConfig := E2EFilebeatConfig

	stackVersion := version.MustParse(test.Ctx().ElasticStackVersion)
	// Stack versions 8.0.X to 8.9.X do not support fingerprint identity type
	// Versions 7.17.X and 8.10.X and above support fingerprint identity type
	if !SupportsFingerprintIdentity(stackVersion) {
		fileBeatConfig = E2EFilebeatConfigPRE810
	}

	fbBuilder = beat.ApplyYamls(t, fbBuilder, fileBeatConfig, E2EFilebeatPodTemplate)

	dashboardCheck := getDashboardCheck(
		esBuilder,
		kbBuilder,
		map[string]bool{
			"Filebeat": true,
		})

	test.Sequence(nil, dashboardCheck, esBuilder, kbBuilder, fbBuilder).RunSequential(t)
}

func TestBeatKibanaRef(t *testing.T) {
	name := "test-beat-kibanaref"

	esBuilder := elasticsearch.NewBuilder(name).
		WithESMasterDataNodes(3, elasticsearch.DefaultResources)

	kbBuilder := kibana.NewBuilder(name).
		WithNodeCount(1).
		WithElasticsearchRef(esBuilder.Ref())

	fbBuilder := beat.NewBuilder(name).
		WithType(filebeat.Type).
		WithRoles(beat.AutodiscoverClusterRoleName).
		WithOpenShiftRoles(test.UseSCCRole).
		WithElasticsearchRef(esBuilder.Ref()).
		WithKibanaRef(kbBuilder.Ref())

	fileBeatConfig := E2EFilebeatConfig

	stackVersion := version.MustParse(test.Ctx().ElasticStackVersion)
	// Stack versions 8.0.X to 8.9.X do not support fingerprint identity type
	// Versions 7.17.X and 8.10.X and above support fingerprint identity type
	if !SupportsFingerprintIdentity(stackVersion) {
		fileBeatConfig = E2EFilebeatConfigPRE810
	}

	fbBuilder = beat.ApplyYamls(t, fbBuilder, fileBeatConfig, E2EFilebeatPodTemplate)

	mbBuilder := beat.NewBuilder(name).
		WithType(metricbeat.Type).
		WithRoles(beat.AutodiscoverClusterRoleName).
		WithOpenShiftRoles(test.UseSCCRole).
		WithElasticsearchRef(esBuilder.Ref()).
		WithKibanaRef(kbBuilder.Ref()).
		WithRoles(beat.MetricbeatClusterRoleName)

	mbBuilder = beat.ApplyYamls(t, mbBuilder, e2eMetricbeatConfig, e2eMetricbeatPodTemplate)

	hbBuilder := beat.NewBuilder(name).
		WithType(heartbeat.Type).
		WithOpenShiftRoles(test.UseSCCRole).
		WithDeployment().
		WithElasticsearchRef(esBuilder.Ref())

	configYaml := fmt.Sprintf(e2eHeartBeatConfigTpl, esv1.HTTPService(esBuilder.Elasticsearch.Name), esBuilder.Elasticsearch.Namespace)

	hbBuilder = beat.ApplyYamls(t, hbBuilder, configYaml, e2eHeartbeatPodTemplate)

	dashboardCheck := getDashboardCheck(
		esBuilder,
		kbBuilder,
		map[string]bool{
			"Filebeat":   true,
			"Metricbeat": true,
			"Heartbeat":  false,
		})

	test.Sequence(nil, dashboardCheck, esBuilder, kbBuilder, fbBuilder, mbBuilder, hbBuilder).RunSequential(t)
}

func getDashboardCheck(esBuilder elasticsearch.Builder, kbBuilder kibana.Builder, beatToDashboardsPresence map[string]bool) test.StepsFunc {
	return func(client *test.K8sClient) test.StepList {
		return test.StepList{
			{
				Name: "Verify dashboards installed",
				Test: test.Eventually(func() error {
					password, err := client.GetElasticPassword(esBuilder.Ref().NamespacedName())
					if err != nil {
						return err
					}

					for beat, expectDashboards := range beatToDashboardsPresence {
						// We are exploiting the fact here that Beats dashboards follow a naming convention that contains the
						// name of the beat. This test will obviously break if future versions of Beats abandon this naming convention.
						query := fmt.Sprintf("/api/saved_objects/_find?type=dashboard&search_fields=title&search=%s", beat)
						body, _, err := kibana.DoRequest(client, kbBuilder.Kibana, password,
							"GET", query, nil, http.Header{},
						)
						if err != nil {
							return err
						}
						var dashboards kbSavedObjects
						if err := json.Unmarshal(body, &dashboards); err != nil {
							return err
						}
						if dashboards.Total == 0 && expectDashboards {
							return fmt.Errorf("expected %s dashboards, but found none", beat)
						}
						if dashboards.Total != 0 && !expectDashboards {
							return fmt.Errorf("expected no %s dashboards, but found some", beat)
						}

					}
					return nil
				}),
			},
		}
	}
}
