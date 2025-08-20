package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bitly/go-simplejson"
	"github.com/go-logr/logr"
	genapi "github.com/grafana/grafana-openapi-client-go/client"
	"github.com/grafana/grafana-openapi-client-go/client/orgs"
	"github.com/grafana/grafana-openapi-client-go/models"
	"github.com/grafana/grafana-operator/v5/api/v1beta1"
	grafanav1beta1 "github.com/grafana/grafana-operator/v5/api/v1beta1"
	client2 "github.com/grafana/grafana-operator/v5/controllers/client"
	"github.com/grafana/grafana-operator/v5/controllers/content"

	"github.com/grafana/grafana-operator/v5/controllers/metrics"
	kuberr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	conditionOrganizationSynchronized = "OrganizationSynchronized"
)

// GrafanaOrganizationReconciler reconciles a GrafanaOrganization object
type GrafanaOrganizationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

//+kubebuilder:rbac:groups=grafana.integreatly.org,resources=grafanaorganizations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=grafana.integreatly.org,resources=grafanaorganizations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=grafana.integreatly.org,resources=grafanaorganizations/finalizers,verbs=update

func (r *GrafanaOrganizationReconciler) syncOrganizations(ctx context.Context) (ctrl.Result, error) {
	syncLog := log.FromContext(ctx).WithName("GrafanaOrganizationReconciler")
	syncLog.Info("syncOrganizations")
	organizationsSynced := 0

	// get all grafana instances
	grafanas := &grafanav1beta1.GrafanaList{}
	var opts []client.ListOption
	err := r.Client.List(ctx, grafanas, opts...)
	if err != nil {
		return ctrl.Result{
			Requeue: true,
		}, err
	}

	// no instances, no need to sync
	if len(grafanas.Items) == 0 {
		return ctrl.Result{Requeue: false}, nil
	}

	// get all organizations
	allOrganizations := &grafanav1beta1.GrafanaOrganizationList{}
	err = r.Client.List(ctx, allOrganizations, opts...)
	if err != nil {
		return ctrl.Result{
			Requeue: true,
		}, err
	}

	// sync organizations, delete organizations from grafana that do no longer have a cr
	organizationsToDelete := map[*grafanav1beta1.Grafana]grafanav1beta1.NamespacedResourceList{}
	for _, grafana := range grafanas.Items {
		grafana := grafana

		for _, organization := range grafana.Status.Organizations {
			orgNs, orgName, _ := organization.Split()
			if !allOrganizations.Exists(orgNs, orgName) {
				organizationsToDelete[&grafana] = append(organizationsToDelete[&grafana], organization)
			}
		}
	}

	// delete all organizations that no longer have a cr
	for grafana, existingOrganizationsToBeDeleted := range organizationsToDelete {
		grafana := grafana
		grafanaClient, err := client2.NewGeneratedGrafanaClient(ctx, r.Client, grafana)
		if err != nil {
			return ctrl.Result{Requeue: true}, err
		}

		for _, organization := range existingOrganizationsToBeDeleted {
			// avoid bombarding the grafana instance with a large number of requests at once, limit
			// the sync to ten organizations per cycle. This means that it will take longer to sync
			// a large number of deleted organization crs, but that should be an edge case.
			if organizationsSynced >= syncBatchSize {
				return ctrl.Result{Requeue: true}, nil
			}

			namespace, name, orgName := organization.Split()
			instanceOrganization, err := grafanaClient.Orgs.GetOrgByName(orgName)
			if err != nil {
				if !strings.Contains(err.Error(), "Organization not found") {
					return ctrl.Result{Requeue: false}, err
				}
				syncLog.Info("organization no longer exists", "namespace", namespace, "name", name)
			} else {
				_, err = grafanaClient.Orgs.DeleteOrgByID(instanceOrganization.Payload.ID) //nolint
				if err != nil {
					var notFound *orgs.DeleteOrgByIDNotFound
					if !errors.As(err, &notFound) {
						return ctrl.Result{Requeue: false}, err
					}
				}
			}

			grafana.Status.Organizations = grafana.Status.Organizations.RemoveEntries(&existingOrganizationsToBeDeleted)
			organizationsSynced += 1

		}

		// one update per grafana - this will trigger a reconcile of the grafana controller
		// so we should minimize those updates
		err = r.Client.Status().Update(ctx, grafana)
		if err != nil {
			return ctrl.Result{Requeue: false}, err
		}

	}

	if organizationsSynced > 0 {
		syncLog.Info("successfully synced organizations", "organizations", organizationsSynced)
	}

	return ctrl.Result{}, nil

}

//+kubebuilder:rbac:groups=grafana.integreatly.org,resources=grafanaorganizations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=grafana.integreatly.org,resources=grafanaorganizations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=grafana.integreatly.org,resources=grafanaorganizations/finalizers,verbs=update

func (r *GrafanaOrganizationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) { //nolint:gocyclo
	log := logf.FromContext(ctx).WithName("GrafanaDashboardReconciler")
	ctx = logf.IntoContext(ctx, log)

	if req.Namespace == "" && req.Name == "" {
		start := time.Now()
		syncResult, err := r.syncOrganizations(ctx)
		elapsed := time.Since(start).Milliseconds()
		metrics.InitialOrganisationsSyncDuration.Set(float64(elapsed))
		return syncResult, err
	}

	cr := &v1beta1.GrafanaOrganization{}

	err := r.Get(ctx, req.NamespacedName, cr)
	if err != nil {
		if kuberr.IsNotFound(err) {
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, fmt.Errorf("getting grafana organization cr: %w", err)
	}

	if cr.GetDeletionTimestamp() != nil {
		// Check if resource needs clean up
		if controllerutil.ContainsFinalizer(cr, grafanaFinalizer) {
			if err := r.finalize(ctx, cr); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to finalize GrafanaOrganization: %w", err)
			}

			if err := removeFinalizer(ctx, r.Client, cr); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
			}
		}

		return ctrl.Result{}, nil
	}

	defer UpdateStatus(ctx, r.Client, cr)

	if cr.Spec.Suspend {
		setSuspended(&cr.Status.Conditions, cr.Generation, conditionReasonApplySuspended)
		return ctrl.Result{}, nil
	}

	removeSuspended(&cr.Status.Conditions)

	removeInvalidSpec(&cr.Status.Conditions)

	instances, err := GetScopedMatchingInstances(ctx, r.Client, cr)
	if err != nil {
		setNoMatchingInstancesCondition(&cr.Status.Conditions, cr.Generation, err)
		meta.RemoveStatusCondition(&cr.Status.Conditions, conditionOrganizationSynchronized)
		cr.Status.NoMatchingInstances = true

		return ctrl.Result{}, fmt.Errorf("failed fetching instances: %w", err)
	}

	if len(instances) == 0 {
		setNoMatchingInstancesCondition(&cr.Status.Conditions, cr.Generation, err)
		meta.RemoveStatusCondition(&cr.Status.Conditions, conditionOrganizationSynchronized)
		cr.Status.NoMatchingInstances = true

		return ctrl.Result{}, ErrNoMatchingInstances
	}

	removeNoMatchingInstance(&cr.Status.Conditions)
	cr.Status.NoMatchingInstances = false

	log.Info("found matching Grafana instances for dashboard", "count", len(instances))

	applyHomeErrors := make(map[string]string)
	pluginErrors := make(map[string]string)
	applyErrors := make(map[string]string)

	organization, hash, err := r.getOrganizationContent(ctx, cr)
	if err != nil {
		log.Error(err, "could not retrieve organization contents", "name", cr.Name, "namespace", cr.Namespace)
		return ctrl.Result{RequeueAfter: RequeueDelay}, err
	}

	for _, grafana := range instances {

		// then import the dashboard into the matching grafana instances
		err = r.onOrganizationCreated(ctx, &grafana, cr, organization, hash)
		if err != nil {
			applyErrors[fmt.Sprintf("%s/%s", grafana.Namespace, grafana.Name)] = err.Error()
		}
	}

	allApplyErrors := mergeReconcileErrors(applyErrors, pluginErrors, applyHomeErrors)

	condition := buildSynchronizedCondition("Organization", conditionOrganizationSynchronized, cr.Generation, allApplyErrors, len(instances))
	meta.SetStatusCondition(&cr.Status.Conditions, condition)

	if len(allApplyErrors) > 0 {
		return ctrl.Result{}, fmt.Errorf("failed to apply to all instances: %v", allApplyErrors)
	}

	cr.Status.Hash = hash
	cr.Status.UID = organization.Name

	return ctrl.Result{RequeueAfter: cr.Spec.ResyncPeriod.Duration}, nil
}

//TODO smazat
/*func (r *GrafanaOrganizationReconciler) Reconcilee(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	controllerLog := log.FromContext(ctx).WithName("GrafanaOrganizationReconciler")
	r.Log = controllerLog

	// periodic sync reconcile
	if req.Namespace == "" && req.Name == "" {
		start := time.Now()
		syncResult, err := r.syncOrganizations(ctx)
		elapsed := time.Since(start).Milliseconds()
		metrics.InitialOrganisationsSyncDuration.Set(float64(elapsed))
		return syncResult, err
	}

	cr := &grafanav1beta1.GrafanaOrganization{}
	err := r.Client.Get(ctx, client.ObjectKey{
		Namespace: req.Namespace,
		Name:      req.Name,
	}, cr)
	if err != nil {
		if kuberr.IsNotFound(err) {
			err = r.onOrganizationDeleted(ctx, req.Namespace, req.Name)
			if err != nil {
				return ctrl.Result{RequeueAfter: RequeueDelay}, err
			}
			return ctrl.Result{}, nil
		}
		controllerLog.Error(err, "error getting grafana organization cr")
		return ctrl.Result{RequeueAfter: RequeueDelay}, err
	}

	if cr.Spec.Organization == nil {
		controllerLog.Info("skipped organization with empty spec", cr.Name, cr.Namespace)
		// TODO: add a custom status around that?
		return ctrl.Result{}, nil
	}

	instances, err := GetScopedMatchingInstances(ctx, r.Client, cr)
	if err != nil {
		controllerLog.Error(err, "could not find matching instances", "name", cr.Name, "namespace", cr.Namespace)
		return ctrl.Result{RequeueAfter: RequeueDelay}, err
	}

	controllerLog.Info("found matching Grafana instances for organization", "count", len(instances))

	organization, hash, err := r.getOrganizationContent(ctx, cr)
	if err != nil {
		controllerLog.Error(err, "could not retrieve organization contents", "name", cr.Name, "namespace", cr.Namespace)
		return ctrl.Result{RequeueAfter: RequeueDelay}, err
	}

	success := true
	for _, grafana := range instances {
		// check if this is a cross namespace import
		if grafana.Namespace != cr.Namespace && !cr.Spec.AllowCrossNamespaceImport {
			continue
		}

		grafana := grafana
		// an admin url is required to interact with grafana
		// the instance or route might not yet be ready
		if grafana.Status.Stage != grafanav1beta1.OperatorStageComplete || grafana.Status.StageStatus != grafanav1beta1.OperatorStageResultSuccess {
			controllerLog.Info("grafana instance not ready", "grafana", grafana.Name)
			success = false
			continue
		}

		// then import the organization into the matching grafana instances
		err = r.onOrganizationCreated(ctx, &grafana, cr, organization, hash)
		if err != nil {
			success = false
			cr.Status.LastMessage = err.Error()
			controllerLog.Error(err, "error reconciling organization", "organization", cr.Name, "grafana", grafana.Name)
		}
	}

	// if the organization was successfully synced in all instances, wait for its re-sync period
	if success {
		cr.Status.LastMessage = ""
		cr.Status.Hash = hash
		if cr.ResyncPeriodHasElapsed() {
			cr.Status.LastResync = metav1.Time{Time: time.Now()}
		}
		cr.Status.UID = organization.Name
		return ctrl.Result{RequeueAfter: cr.Spec.ResyncPeriod.Duration}, r.Client.Status().Update(ctx, cr)
	} else {
		// if there was an issue with the organization, update the status
		return ctrl.Result{RequeueAfter: RequeueDelay}, r.Client.Status().Update(ctx, cr)
	}

}*/

func (r *GrafanaOrganizationReconciler) finalize(ctx context.Context, cr *v1beta1.GrafanaOrganization) error {
	log := logf.FromContext(ctx)

	instances, err := GetScopedMatchingInstances(ctx, r.Client, cr)
	if err != nil {
		return fmt.Errorf("fetching instances: %w", err)
	}

	for _, grafana := range instances {
		found, orgName := grafana.Status.Organizations.Find(cr.Namespace, cr.Name)
		if !found {
			continue
		}

		grafanaClient, err := client2.NewGeneratedGrafanaClient(ctx, r.Client, &grafana)
		if err != nil {
			return fmt.Errorf("creating grafana http client: %w", err)
		}

		isCleanupInGrafanaRequired := true

		orgInstance, err := grafanaClient.Orgs.GetOrgByName(*orgName)
		if err != nil {
			var notFound *orgs.DeleteOrgByIDNotFound
			if !errors.As(err, &notFound) {
				return fmt.Errorf("fetching organization from instance: %w", err)
			}

			isCleanupInGrafanaRequired = false
		}

		if isCleanupInGrafanaRequired {

			_, err = grafanaClient.Orgs.DeleteOrgByID(orgInstance.Payload.ID) //nolint
			if err != nil {
				if !strings.Contains(err.Error(), "ID not found") {
					return err
				}
			}
			log.Info("organization deleted ", "instance", &grafana.Name, "org", orgName)
		}

		// Update grafana instance Status
		err = grafana.RemoveNamespacedResource(ctx, r.Client, cr)
		if err != nil {
			return fmt.Errorf("removing organization from grafana cr: %w", err)
		}
	}

	return nil
}

//TODO smazat
/*func (r *GrafanaOrganizationReconciler) onOrganizationDeleted(ctx context.Context, namespace string, name string) error {
	log := log.FromContext(ctx).WithName("GrafanaOrganizationReconciler")
	log.Info("onOrganizationDeleted")
	list := grafanav1beta1.GrafanaList{}
	opts := []client.ListOption{}
	err := r.Client.List(ctx, &list, opts...)
	if err != nil {
		return err
	}

	for _, grafana := range list.Items {
		grafana := grafana
		if found, orgName := grafana.Status.Organizations.Find(namespace, name); found {
			grafanaClient, err := client2.NewGeneratedGrafanaClient(ctx, r.Client, &grafana)
			if err != nil {
				return err
			}

			orgInstance, err := grafanaClient.Orgs.GetOrgByName(*orgName)
			if err != nil {
				if !strings.Contains(err.Error(), "Organization not found") {
					return err
				}
			} else if orgInstance != nil {
				_, err = grafanaClient.Orgs.DeleteOrgByID(orgInstance.Payload.ID) //nolint
				if err != nil {
					if !strings.Contains(err.Error(), "ID not found") {
						return err
					}
				}
			}

			// Update grafana instance Status
			err = grafana.RemoveNamespacedResource(ctx, r.Client, cr)
			if err != nil {
				return fmt.Errorf("removing dashboard from grafana cr: %w", err)
			}

		}
	}

	return nil

}*/

func (r *GrafanaOrganizationReconciler) onOrganizationCreated(ctx context.Context, grafana *grafanav1beta1.Grafana, cr *grafanav1beta1.GrafanaOrganization, organization *models.UpdateOrgForm, hash string) error {

	if cr.Spec.Organization == nil {
		return nil
	}

	grafanaClient, err := client2.NewGeneratedGrafanaClient(ctx, r.Client, grafana)
	if err != nil {
		return err
	}

	exists, id, err := r.Exists(grafanaClient, organization.Name)
	if err != nil {
		return err
	}

	if exists && content.Unchanged(cr, hash) {
		return nil
	}

	encoded, err := json.Marshal(organization)
	if err != nil {
		return fmt.Errorf("representing organization as JSON: %w", err)
	}
	if exists {
		var body models.UpdateOrgForm
		if err := json.Unmarshal(encoded, &body); err != nil {
			return fmt.Errorf("representing organization as update command: %w", err)
		}
		//organization.UID = uid
		_, err := grafanaClient.Orgs.UpdateOrg(id, &body)
		//_, err := grafanaClient.Datasources.UpdateDataSourceByUID(organization.UID, &body) //nolint
		if err != nil {
			return err
		}
	} else {
		var body models.CreateOrgCommand

		if err := json.Unmarshal(encoded, &body); err != nil {
			return fmt.Errorf("representing organization as create command: %w", err)
		}
		orgCreatedResponse, err := grafanaClient.Orgs.CreateOrg(&body) //nolint
		if err != nil {
			return err
		}
		id = *orgCreatedResponse.Payload.OrgID
	}

	// Update grafana instance Status
	return grafana.AddNamespacedResource(ctx, r.Client, cr, cr.NamespacedResource(organization.Name))

}

func (r *GrafanaOrganizationReconciler) Exists(client *genapi.GrafanaHTTPAPI, name string) (bool, int64, error) {
	organization, err := client.Orgs.GetOrgByName(name)
	if err != nil && !strings.Contains(err.Error(), "(status 404)") {
		return false, 0, fmt.Errorf("fetching organization: %w", err)
	}

	if organization != nil {
		return true, organization.Payload.ID, nil
	}

	return false, 0, nil

}

func (r *GrafanaOrganizationReconciler) SetupWithManager(mgr ctrl.Manager, ctx context.Context) error {

	return ctrl.NewControllerManagedBy(mgr).
		For(&grafanav1beta1.GrafanaOrganization{}).
		WithEventFilter(ignoreStatusUpdates()).
		Complete(r)
}

func (r *GrafanaOrganizationReconciler) getOrganizationContent(ctx context.Context, cr *grafanav1beta1.GrafanaOrganization) (*models.UpdateOrgForm, string, error) {
	initialBytes, err := json.Marshal(cr.Spec.Organization)
	if err != nil {
		return nil, "", err
	}

	simpleContent, err := simplejson.NewJson(initialBytes)
	if err != nil {
		return nil, "", err
	}

	/*if cr.Spec.Organization.UID == "" {
		simpleContent.Set("uid", string(cr.UID))
	}*/

	newBytes, err := simpleContent.MarshalJSON()
	if err != nil {
		return nil, "", err
	}

	// We use UpdateOrgForm here because models.DataSource lacks the SecureJsonData field
	var res models.UpdateOrgForm
	if err = json.Unmarshal(newBytes, &res); err != nil {
		return nil, "", err
	}

	hash := sha256.New()
	hash.Write(newBytes)

	return &res, fmt.Sprintf("%x", hash.Sum(nil)), nil
}
