package phases

import (
	"context"
	"errors"

	"github.com/databricks/cli/bundle"
	"github.com/databricks/cli/bundle/apps"
	"github.com/databricks/cli/bundle/artifacts"
	"github.com/databricks/cli/bundle/config"
	"github.com/databricks/cli/bundle/config/mutator"
	"github.com/databricks/cli/bundle/deploy"
	"github.com/databricks/cli/bundle/deploy/files"
	"github.com/databricks/cli/bundle/deploy/lock"
	"github.com/databricks/cli/bundle/deploy/metadata"
	"github.com/databricks/cli/bundle/deploy/terraform"
	"github.com/databricks/cli/bundle/deployplan"
	"github.com/databricks/cli/bundle/libraries"
	"github.com/databricks/cli/bundle/metrics"
	"github.com/databricks/cli/bundle/permissions"
	"github.com/databricks/cli/bundle/scripts"
	"github.com/databricks/cli/bundle/statemgmt"
	"github.com/databricks/cli/bundle/terranova"
	"github.com/databricks/cli/bundle/trampoline"
	"github.com/databricks/cli/libs/cmdio"
	"github.com/databricks/cli/libs/diag"
	"github.com/databricks/cli/libs/log"
	"github.com/databricks/cli/libs/sync"
)

func approvalForDeploy(ctx context.Context, b *bundle.Bundle) (bool, error) {
	var actions []deployplan.Action
	var err error

	if b.DirectDeployment {
		actions, err = terranova.CalculateDeployActions(ctx, b)
		if err != nil {
			return false, err
		}
	} else {
		tf := b.Terraform
		if tf == nil {
			return false, errors.New("terraform not initialized")
		}
		actions, err = terraform.ShowPlanFile(ctx, tf, b.Plan.TerraformPlanPath)
		if err != nil {
			return false, err
		}
	}

	b.Plan.Actions = actions

	types := []deployplan.ActionType{deployplan.ActionTypeRecreate, deployplan.ActionTypeDelete}
	schemaActions := deployplan.FilterGroup(actions, "schemas", types...)
	dltActions := deployplan.FilterGroup(actions, "pipelines", types...)
	volumeActions := deployplan.FilterGroup(actions, "volumes", types...)
	dashboardActions := deployplan.FilterGroup(actions, "dashboards", types...)

	// We don't need to display any prompts in this case.
	if len(schemaActions) == 0 && len(dltActions) == 0 && len(volumeActions) == 0 && len(dashboardActions) == 0 {
		return true, nil
	}

	// One or more UC schema resources will be deleted or recreated.
	if len(schemaActions) != 0 {
		cmdio.LogString(ctx, "The following UC schemas will be deleted or recreated. Any underlying data may be lost:")
		for _, action := range schemaActions {
			cmdio.Log(ctx, action)
		}
	}

	// One or more DLT pipelines is being recreated.
	if len(dltActions) != 0 {
		msg := `
This action will result in the deletion or recreation of the following DLT Pipelines along with the
Streaming Tables (STs) and Materialized Views (MVs) managed by them. Recreating the Pipelines will
restore the defined STs and MVs through full refresh. Note that recreation is necessary when pipeline
properties such as the 'catalog' or 'storage' are changed:`
		cmdio.LogString(ctx, msg)
		for _, action := range dltActions {
			cmdio.Log(ctx, action)
		}
	}

	// One or more volumes is being recreated.
	if len(volumeActions) != 0 {
		msg := `
This action will result in the deletion or recreation of the following volumes.
For managed volumes, the files stored in the volume are also deleted from your
cloud tenant within 30 days. For external volumes, the metadata about the volume
is removed from the catalog, but the underlying files are not deleted:`
		cmdio.LogString(ctx, msg)
		for _, action := range volumeActions {
			cmdio.Log(ctx, action)
		}
	}

	// One or more dashboards is being recreated.
	if len(dashboardActions) != 0 {
		msg := `
This action will result in the deletion or recreation of the following dashboards.
This will result in changed IDs and permanent URLs of the dashboards that will be recreated:`
		cmdio.LogString(ctx, msg)
		for _, action := range dashboardActions {
			cmdio.Log(ctx, action)
		}
	}

	if b.AutoApprove {
		return true, nil
	}

	if !cmdio.IsPromptSupported(ctx) {
		return false, errors.New("the deployment requires destructive actions, but current console does not support prompting. Please specify --auto-approve if you would like to skip prompts and proceed")
	}

	cmdio.LogString(ctx, "")
	approved, err := cmdio.AskYesOrNo(ctx, "Would you like to proceed?")
	if err != nil {
		return false, err
	}

	return approved, nil
}

func deployCore(ctx context.Context, b *bundle.Bundle) diag.Diagnostics {
	// Core mutators that CRUD resources and modify deployment state. These
	// mutators need informed consent if they are potentially destructive.
	cmdio.LogString(ctx, "Deploying resources...")

	var diags diag.Diagnostics

	if b.DirectDeployment {
		diags = bundle.Apply(ctx, b, terranova.TerranovaApply())
	} else {
		diags = bundle.Apply(ctx, b, terraform.Apply())
	}

	// Even if deployment failed, there might be updates in states that we need to upload
	newDiags := bundle.Apply(ctx, b,
		statemgmt.StatePush(),
	)
	diags = diags.Extend(newDiags)
	if newDiags.HasError() {
		return diags
	}

	if b.DirectDeployment {
		// TODO: terraform.Load alternative
	} else {
		newDiags := bundle.Apply(ctx, b,
			terraform.Load(),
		)
		diags = diags.Extend(newDiags)
		if newDiags.HasError() {
			return diags
		}
	}

	diags = diags.Extend(bundle.ApplySeq(ctx, b,
		apps.InterpolateVariables(),
		apps.UploadConfig(),
		metadata.Compute(),
		metadata.Upload(),
	))

	if !diags.HasError() {
		cmdio.LogString(ctx, "Deployment complete!")
	}

	return diags
}

// The deploy phase deploys artifacts and resources.
func Deploy(ctx context.Context, b *bundle.Bundle, outputHandler sync.OutputHandler) (diags diag.Diagnostics) {
	log.Info(ctx, "Phase: deploy")

	// Core mutators that CRUD resources and modify deployment state. These
	// mutators need informed consent if they are potentially destructive.
	diags = bundle.ApplySeq(ctx, b,
		scripts.Execute(config.ScriptPreDeploy),
		lock.Acquire(),
	)

	if diags.HasError() {
		// lock is not acquired here
		return diags
	}

	// lock is acquired here
	defer func() {
		diags = diags.Extend(bundle.Apply(ctx, b, lock.Release(lock.GoalDeploy)))
	}()

	// TODO: StatePull and OpenResourceDatabase both parse resources.json; we should do it only once

	diags = diags.Extend(bundle.ApplySeq(ctx, b,
		statemgmt.StatePull(),
		terraform.CheckDashboardsModifiedRemotely(),
		deploy.StatePull(),
		mutator.ValidateGitDetails(),
		terraform.CheckRunningResource(),
		artifacts.CleanUp(),
		// libraries.CheckForSameNameLibraries() needs to be run after we expand glob references so we
		// know what are the actual library paths.
		// libraries.ExpandGlobReferences() has to be run after the libraries are built and thus this
		// mutator is part of the deploy step rather than validate.
		libraries.ExpandGlobReferences(),
		libraries.CheckForSameNameLibraries(),
		// SwitchToPatchedWheels must be run after ExpandGlobReferences and after build phase because it Artifact.Source and Artifact.Patched populated
		libraries.SwitchToPatchedWheels(),
		libraries.Upload(),
		trampoline.TransformWheelTask(),
		files.Upload(outputHandler),
		deploy.StateUpdate(),
		deploy.StatePush(),
		permissions.ApplyWorkspaceRootPermissions(),
		metrics.TrackUsedCompute(),
	))

	if diags.HasError() {
		return diags
	}

	if b.DirectDeployment {
		err := b.OpenResourceDatabase(ctx)
		if err != nil {
			diags = diags.Extend(diag.FromErr(err))
			return diags
		}
	} else {
		diags = diags.Extend(bundle.ApplySeq(ctx, b,
			terraform.Interpolate(),
			terraform.Write(),
			terraform.Plan(terraform.PlanGoal("deploy")),
		))
	}

	if diags.HasError() {
		return diags
	}

	haveApproval, err := approvalForDeploy(ctx, b)
	if err != nil {
		diags = diags.Extend(diag.FromErr(err))
		return diags
	}

	if haveApproval {
		diags = diags.Extend(deployCore(ctx, b))
	} else {
		cmdio.LogString(ctx, "Deployment cancelled!")
	}

	if diags.HasError() {
		return diags
	}

	logDeployTelemetry(ctx, b)
	return diags.Extend(bundle.Apply(ctx, b, scripts.Execute(config.ScriptPostDeploy)))
}

// If there are more than 1 thousand of a resource type, do not
// include more resources.
// Since we have a timeout of 3 seconds, we cap the maximum number of IDs
// we send in a single request to have reliable telemetry.
const ResourceIdLimit = 1000
