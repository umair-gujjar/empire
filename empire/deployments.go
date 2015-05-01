package empire

import (
	"fmt"
	"time"

	"github.com/remind101/pkg/timex"
	"golang.org/x/net/context"
	"gopkg.in/gorp.v1"
)

// Deployment statuses.
const (
	StatusPending = "pending"
	StatusFailed  = "failed"
	StatusSuccess = "success"
)

// Deployment represents a deployment to the platform.
type Deployment struct {
	ID         string    `db:"id"`
	AppName    string    `db:"app_id"`
	Status     string    `db:"status"`
	Image      Image     `db:"image"`
	Error      *string   `db:"error"`
	ReleaseID  *string   `db:"release_id"`
	CreatedAt  time.Time `db:"created_at"`
	FinishedAt time.Time `db:"finished_at"`

	// Used to store the old status when changing statuses.
	prevStatus string `db:"-"`
}

// PreInsert implements a pre insert hook for the db interface
func (d *Deployment) PreInsert(s gorp.SqlExecutor) error {
	d.CreatedAt = timex.Now()
	return nil
}

// Success marks the deployment as successful. The release provided will be
// associated with this deployment.
func (d *Deployment) Success(release *Release) *Deployment {
	d.ReleaseID = &release.ID
	d.finished(StatusSuccess)
	return d
}

// Failed marks the deployment as failed. An error can be provided, which should
// indicate what went wrong.
func (d *Deployment) Failed(err error) *Deployment {
	e := err.Error()
	d.Error = &e
	d.finished(StatusFailed)
	return d
}

func (d *Deployment) finished(status string) {
	d.FinishedAt = timex.Now()
	d.changeStatus(status)
}

func (d *Deployment) changeStatus(status string) {
	d.prevStatus, d.Status = d.Status, status
}

// DeploymentsCreateOpts represents options that can be passed when creating a
// new Deployment.
type DeploymentsCreateOpts struct {
	// App is the app that is being deployed to.
	App *App

	// Image is the image that's being deployed.
	Image Image

	// EventCh will receive deployment events during deployment.
	EventCh chan Event
}

func (s *store) DeploymentsCreate(opts DeploymentsCreateOpts) (*Deployment, error) {
	d := &Deployment{
		AppName: opts.App.Name,
		Image:   opts.Image,
		Status:  StatusPending,
	}
	return deploymentsCreate(s.db, d)
}

func (s *store) DeploymentsUpdate(d *Deployment) error {
	return deploymentsUpdate(s.db, d)
}

type deployer struct {
	store *store

	*appsService
	*configsService
	*slugsService
	*releasesService
}

// DeploymentsDo performs the Deployment.
func (s *deployer) DeploymentsDo(ctx context.Context, opts DeploymentsCreateOpts) (d *Deployment, err error) {
	app, image := opts.App, opts.Image

	d, err = s.store.DeploymentsCreate(opts)
	if err != nil {
		return
	}

	var (
		config  *Config
		slug    *Slug
		release *Release
	)

	defer func() {
		if err == nil {
			d.Success(release)
		} else {
			d.Failed(err)
		}

		if err2 := s.store.DeploymentsUpdate(d); err2 != nil {
			err = err2
		}

		return
	}()

	// Grab the latest config.
	config, err = s.ConfigsCurrent(app)
	if err != nil {
		return
	}

	// Create a new slug for the docker image.
	slug, err = s.SlugsCreateByImage(image, opts.EventCh)
	if err != nil {
		return
	}

	// Create a new release for the Config
	// and Slug.
	desc := fmt.Sprintf("Deploy %s", image.String())
	release, err = s.ReleasesCreate(ctx, ReleasesCreateOpts{
		App:         app,
		Config:      config,
		Slug:        slug,
		Description: desc,
	})
	if err != nil {
		return
	}

	return
}

func (s *deployer) DeployImageToApp(ctx context.Context, app *App, image Image, out chan Event) (*Deployment, error) {
	if err := s.appsService.AppsEnsureRepo(app, image.Repo); err != nil {
		return nil, err
	}

	return s.DeploymentsDo(ctx, DeploymentsCreateOpts{
		App:     app,
		Image:   image,
		EventCh: out,
	})
}

// Deploy deploys an Image to the cluster.
func (s *deployer) DeployImage(ctx context.Context, image Image, out chan Event) (*Deployment, error) {
	app, err := s.appsService.AppsFindOrCreateByRepo(image.Repo)
	if err != nil {
		return nil, err
	}

	return s.DeployImageToApp(ctx, app, image, out)
}

func deploymentsCreate(db *db, d *Deployment) (*Deployment, error) {
	return d, db.Insert(d)
}

func deploymentsUpdate(db *db, d *Deployment) error {
	_, err := db.Update(d)
	return err
}
