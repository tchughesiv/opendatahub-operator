// Package feastoperator provides utility functions to config Feast Operator as part of the stack
// ??????????? which makes managing distributed compute infrastructure in the cloud easy and intuitive for Data Scientists
// +groupName=datasciencecluster.opendatahub.io
package feastoperator

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/go-logr/logr"
	operatorv1 "github.com/openshift/api/operator/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dsciv1 "github.com/opendatahub-io/opendatahub-operator/v2/apis/dscinitialization/v1"
	"github.com/opendatahub-io/opendatahub-operator/v2/components"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/cluster"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/deploy"
)

var (
	ComponentName = "feastoperator"
	Path          = deploy.DefaultManifestPath + "/" + ComponentName + "/default"
)

// Verifies that FeastOperator implements ComponentInterface.
var _ components.ComponentInterface = (*FeastOperator)(nil)

// FeastOperator struct holds the configuration for the Feast Operator component.
// +kubebuilder:object:generate=true
type FeastOperator struct {
	components.Component `json:""`
}

func (d *FeastOperator) OverrideManifests(ctx context.Context, platform cluster.Platform) error {
	// If devflags are set, update default manifests path
	if len(d.DevFlags.Manifests) != 0 {
		manifestConfig := d.DevFlags.Manifests[0]
		if err := deploy.DownloadManifests(ctx, ComponentName, manifestConfig); err != nil {
			return err
		}
		// If overlay is defined, update paths
		defaultKustomizePath := "default"
		if manifestConfig.SourcePath != "" {
			defaultKustomizePath = manifestConfig.SourcePath
		}
		Path = filepath.Join(deploy.DefaultManifestPath, ComponentName, defaultKustomizePath)
	}

	return nil
}

func (d *FeastOperator) GetComponentName() string {
	return ComponentName
}

func (d *FeastOperator) ReconcileComponent(ctx context.Context,
	cli client.Client,
	logger logr.Logger,
	owner metav1.Object,
	dscispec *dsciv1.DSCInitializationSpec,
	platform cluster.Platform,
	_ bool,
) error {
	l := d.ConfigComponentLogger(logger, ComponentName, dscispec)
	var imageParamMap = map[string]string{
		"odh-feast-operator-controller-image": "RELATED_IMAGE_ODH_FEAST_OPERATOR_IMAGE",
		"namespace":                           dscispec.ApplicationsNamespace,
	}

	enabled := d.GetManagementState() == operatorv1.Managed
	monitoringEnabled := dscispec.Monitoring.ManagementState == operatorv1.Managed

	if enabled {
		if d.DevFlags != nil {
			// Download manifests and update paths
			if err := d.OverrideManifests(ctx, platform); err != nil {
				return err
			}
		}

		// skip check if the dependent operator has been installed, this is done in dashboard
		// Update image parameters only when we do not have customized manifests set
		if (dscispec.DevFlags == nil || dscispec.DevFlags.ManifestsUri == "") && (d.DevFlags == nil || len(d.DevFlags.Manifests) == 0) {
			if err := deploy.ApplyParams(Path, imageParamMap); err != nil {
				return fmt.Errorf("failed to update image from %s : %w", Path, err)
			}
		}
	}

	if err := deploy.DeployManifestsFromPath(ctx, cli, owner, Path, dscispec.ApplicationsNamespace, ComponentName, enabled); err != nil {
		return err
	}
	l.Info("apply manifests done")

	// CloudService Monitoring handling
	if platform == cluster.ManagedRhods {
		if enabled {
			// first check if the service is up, so prometheus won't fire alerts when it is just startup
			// only 1 replica should be very quick
			if err := cluster.WaitForDeploymentAvailable(ctx, cli, ComponentName, dscispec.ApplicationsNamespace, 10, 1); err != nil {
				return fmt.Errorf("deployment for %s is not ready to server: %w", ComponentName, err)
			}
			l.Info("deployment is done, updating monitoring rules")
		}

		if err := d.UpdatePrometheusConfig(cli, l, enabled && monitoringEnabled, ComponentName); err != nil {
			return err
		}
		if err := deploy.DeployManifestsFromPath(ctx, cli, owner,
			filepath.Join(deploy.DefaultManifestPath, "monitoring", "prometheus", "apps"),
			dscispec.Monitoring.Namespace,
			"prometheus", true); err != nil {
			return err
		}
		l.Info("updating SRE monitoring done")
	}

	return nil
}
