package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"github.com/pokt-foundation/pocket-http-db/cache"
	"github.com/pokt-foundation/portal-api-go/repository"
	jsonresponse "github.com/pokt-foundation/utils-go/json-response"
	"github.com/sirupsen/logrus"
)

var (
	errNoPayFound          = errors.New("pay plan not found")
	errBalancerNotFound    = errors.New("load balancer not found")
	errBlockchainNotFound  = errors.New("blockchain not found")
	errApplicationNotFound = errors.New("applications not found")
)

// Writer represents the implementation of writer interface
type Writer interface {
	WriteLoadBalancer(loadBalancer *repository.LoadBalancer) (*repository.LoadBalancer, error)
	UpdateLoadBalancer(id string, options *repository.UpdateLoadBalancer) error
	RemoveLoadBalancer(id string) error
	WriteApplication(app *repository.Application) (*repository.Application, error)
	UpdateApplication(id string, options *repository.UpdateApplication) error
	UpdateFirstDateSurpassed(firstDateSurpassed *repository.UpdateFirstDateSurpassed) error
	RemoveApplication(id string) error
	WriteBlockchain(blockchain *repository.Blockchain) (*repository.Blockchain, error)
	WriteRedirect(redirect *repository.Redirect) (*repository.Redirect, error)
	ActivateBlockchain(id string, active bool) error
}

// Router struct handler for router requests
type Router struct {
	Cache   *cache.Cache
	Router  *mux.Router
	Writer  Writer
	APIKeys map[string]bool
	log     *logrus.Logger
}

func (rt *Router) logError(err error) {
	fields := logrus.Fields{
		"err": err.Error(),
	}

	rt.log.WithFields(fields).Error(err)
}

// NewRouter returns router instance
func NewRouter(reader cache.Reader, writer Writer, apiKeys map[string]bool, logger *logrus.Logger) (*Router, error) {
	cache := cache.NewCache(reader, logger)

	err := cache.SetCache()
	if err != nil {
		return nil, err
	}

	rt := &Router{
		Cache:   cache,
		Writer:  writer,
		Router:  mux.NewRouter(),
		APIKeys: apiKeys,
		log:     logger,
	}

	rt.Router.HandleFunc("/", rt.HealthCheck).Methods(http.MethodGet)
	rt.Router.HandleFunc("/blockchain", rt.GetBlockchains).Methods(http.MethodGet)
	rt.Router.HandleFunc("/blockchain", rt.CreateBlockchain).Methods(http.MethodPost)
	rt.Router.HandleFunc("/blockchain/{id}", rt.GetBlockchain).Methods(http.MethodGet)
	rt.Router.HandleFunc("/blockchain/{id}/activate", rt.ActivateBlockchain).Methods(http.MethodPost)
	rt.Router.HandleFunc("/application", rt.GetApplications).Methods(http.MethodGet)
	rt.Router.HandleFunc("/application", rt.CreateApplication).Methods(http.MethodPost)
	rt.Router.HandleFunc("/application/limits", rt.GetApplicationsLimits).Methods(http.MethodGet)
	rt.Router.HandleFunc("/application/{id}", rt.GetApplication).Methods(http.MethodGet)
	rt.Router.HandleFunc("/application/{id}", rt.UpdateApplication).Methods(http.MethodPut)
	rt.Router.HandleFunc("/application/first_date_surpassed", rt.UpdateFirstDateSurpassed).Methods(http.MethodPost)
	rt.Router.HandleFunc("/load_balancer", rt.GetLoadBalancers).Methods(http.MethodGet)
	rt.Router.HandleFunc("/load_balancer", rt.CreateLoadBalancer).Methods(http.MethodPost)
	rt.Router.HandleFunc("/load_balancer/{id}", rt.GetLoadBalancer).Methods(http.MethodGet)
	rt.Router.HandleFunc("/load_balancer/{id}", rt.UpdateLoadBalancer).Methods(http.MethodPut)
	rt.Router.HandleFunc("/user/{id}/application", rt.GetApplicationByUserID).Methods(http.MethodGet)
	rt.Router.HandleFunc("/user/{id}/load_balancer", rt.GetLoadBalancerByUserID).Methods(http.MethodGet)
	rt.Router.HandleFunc("/pay_plan", rt.GetPayPlans).Methods(http.MethodGet)
	rt.Router.HandleFunc("/pay_plan/{type}", rt.GetPayPlan).Methods(http.MethodGet)
	rt.Router.HandleFunc("/redirect", rt.CreateRedirect).Methods(http.MethodPost)

	rt.Router.Use(rt.AuthorizationHandler)

	return rt, nil
}

func (rt *Router) AuthorizationHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This is the path of the health check endpoint
		if r.URL.Path == "/" {
			h.ServeHTTP(w, r)

			return
		}

		if !rt.APIKeys[r.Header.Get("Authorization")] {
			w.WriteHeader(http.StatusUnauthorized)
			_, err := w.Write([]byte("Unauthorized"))
			if err != nil {
				panic(err)
			}

			return
		}

		h.ServeHTTP(w, r)
	})
}

func (rt *Router) HealthCheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte("Pocket HTTP DB is up and running!"))
	if err != nil {
		panic(err)
	}
}

func (rt *Router) GetApplications(w http.ResponseWriter, r *http.Request) {
	jsonresponse.RespondWithJSON(w, http.StatusOK, rt.Cache.GetApplications())
}

func (rt *Router) GetApplicationsLimits(w http.ResponseWriter, r *http.Request) {
	apps := rt.Cache.GetApplications()

	var appsLimits []repository.AppLimits

	for _, app := range apps {
		limits := app.Limits

		limits.AppID = app.ID
		limits.AppName = app.Name
		limits.AppUserID = app.UserID
		limits.PublicKey = app.GatewayAAT.ApplicationPublicKey
		limits.NotificationSettings = &app.NotificationSettings

		if !app.FirstDateSurpassed.IsZero() {
			limits.FirstDateSurpassed = &app.FirstDateSurpassed
		}

		appsLimits = append(appsLimits, limits)
	}

	jsonresponse.RespondWithJSON(w, http.StatusOK, appsLimits)
}

func (rt *Router) GetApplication(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	app := rt.Cache.GetApplication(vars["id"])

	if app == nil {
		jsonresponse.RespondWithError(w, http.StatusNotFound, errApplicationNotFound.Error())
		return
	}

	jsonresponse.RespondWithJSON(w, http.StatusOK, app)
}

func (rt *Router) CreateApplication(w http.ResponseWriter, r *http.Request) {
	var app repository.Application

	decoder := json.NewDecoder(r.Body)

	err := decoder.Decode(&app)
	if err != nil {
		jsonresponse.RespondWithError(w, http.StatusBadRequest, err.Error())
		return
	}

	defer r.Body.Close()
	fullApp, err := rt.Writer.WriteApplication(&app)
	if err != nil {
		rt.logError(fmt.Errorf("WriteApplication in CreateApplication failed: %w", errApplicationNotFound))
		jsonresponse.RespondWithError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if fullApp.PayPlanType != "" {
		newPlan := rt.Cache.GetPayPlan(fullApp.PayPlanType)
		fullApp.Limits = repository.AppLimits{
			PlanType:   newPlan.PlanType,
			DailyLimit: newPlan.DailyLimit,
		}

		fullApp.PayPlanType = "" // set to empty to avoid two sources of truth
	}

	jsonresponse.RespondWithJSON(w, http.StatusOK, fullApp)
}

func (rt *Router) UpdateApplication(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	app := rt.Cache.GetApplication(vars["id"])
	if app == nil {
		rt.logError(fmt.Errorf("GetApplication in UpdateApplication failed: %w", errApplicationNotFound))
		jsonresponse.RespondWithError(w, http.StatusNotFound, errApplicationNotFound.Error())
		return
	}

	var updateInput repository.UpdateApplication

	decoder := json.NewDecoder(r.Body)

	err := decoder.Decode(&updateInput)
	if err != nil {
		jsonresponse.RespondWithError(w, http.StatusBadRequest, err.Error())
		return
	}

	defer r.Body.Close()

	if updateInput.Remove {
		err = rt.Writer.RemoveApplication(vars["id"])
		if err != nil {
			rt.logError(fmt.Errorf("RemoveApplication in UpdateApplication failed: %w", err))
			jsonresponse.RespondWithError(w, http.StatusInternalServerError, err.Error())
			return
		}

		app.Status = repository.AwaitingGracePeriod
	} else {
		err = rt.Writer.UpdateApplication(vars["id"], &updateInput)
		if err != nil {
			rt.logError(fmt.Errorf("UpdateApplication failed: %w", err))
			jsonresponse.RespondWithError(w, http.StatusInternalServerError, err.Error())
			return
		}

		if updateInput.Name != "" {
			app.Name = updateInput.Name
		}
		if updateInput.Status != "" {
			app.Status = updateInput.Status
		}
		if updateInput.PayPlanType != "" {
			newPlan := rt.Cache.GetPayPlan(updateInput.PayPlanType)
			app.Limits = repository.AppLimits{
				PlanType:   newPlan.PlanType,
				DailyLimit: newPlan.DailyLimit,
			}
		}
		if !updateInput.FirstDateSurpassed.IsZero() {
			app.FirstDateSurpassed = updateInput.FirstDateSurpassed
		}
		if updateInput.GatewaySettings != nil {
			app.GatewaySettings = *updateInput.GatewaySettings
		}
		if updateInput.NotificationSettings != nil {
			app.NotificationSettings = *updateInput.NotificationSettings
		}
	}

	jsonresponse.RespondWithJSON(w, http.StatusOK, app)
}

func (rt *Router) UpdateFirstDateSurpassed(w http.ResponseWriter, r *http.Request) {
	var updateInput repository.UpdateFirstDateSurpassed

	decoder := json.NewDecoder(r.Body)

	err := decoder.Decode(&updateInput)
	if err != nil {
		rt.logError(fmt.Errorf("UpdateFirstDateSurpassed decode failed: %w", err))
		jsonresponse.RespondWithError(w, http.StatusBadRequest, err.Error())
		return
	}

	defer r.Body.Close()

	if len(updateInput.ApplicationIDs) == 0 {
		jsonresponse.RespondWithError(w, http.StatusBadRequest, "no application IDs on input")
		return
	}

	var appsToUpdate []*repository.Application

	for _, appID := range updateInput.ApplicationIDs {
		app := rt.Cache.GetApplication(appID)
		if app == nil {
			jsonresponse.RespondWithError(w, http.StatusNotFound, fmt.Sprintf("%s not found", appID))
			return
		}

		appsToUpdate = append(appsToUpdate, app)
	}

	err = rt.Writer.UpdateFirstDateSurpassed(&updateInput)
	if err != nil {
		rt.logError(fmt.Errorf("UpdateFirstDateSurpassed failed: %W", err))
		jsonresponse.RespondWithError(w, http.StatusInternalServerError, err.Error())
		return
	}

	for _, app := range appsToUpdate {
		app.FirstDateSurpassed = updateInput.FirstDateSurpassed
	}

	jsonresponse.RespondWithJSON(w, http.StatusOK, appsToUpdate)
}

func (rt *Router) GetApplicationByUserID(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	apps := rt.Cache.GetApplicationsByUserID(vars["id"])

	if len(apps) == 0 {
		rt.logError(fmt.Errorf("GetLoadBalancerByUserID failed: %w", errApplicationNotFound))
		jsonresponse.RespondWithError(w, http.StatusNotFound, errApplicationNotFound.Error())
		return
	}

	jsonresponse.RespondWithJSON(w, http.StatusOK, apps)
}

func (rt *Router) GetLoadBalancerByUserID(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	lbs := rt.Cache.GetLoadBalancersByUserID(vars["id"])

	if len(lbs) == 0 {
		rt.logError(fmt.Errorf("GetLoadBalancerByUserID failed: %w", errBalancerNotFound))
		jsonresponse.RespondWithError(w, http.StatusNotFound, errBalancerNotFound.Error())
		return
	}

	jsonresponse.RespondWithJSON(w, http.StatusOK, lbs)
}

func (rt *Router) GetBlockchain(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	blockchain := rt.Cache.GetBlockchain(vars["id"])

	if blockchain == nil {
		rt.logError(fmt.Errorf("GetBlockchain failed: %w", errBlockchainNotFound))
		jsonresponse.RespondWithError(w, http.StatusNotFound, errBlockchainNotFound.Error())
		return
	}

	jsonresponse.RespondWithJSON(w, http.StatusOK, blockchain)
}

func (rt *Router) ActivateBlockchain(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	blockchainID := vars["id"]

	var active bool

	decoder := json.NewDecoder(r.Body)

	err := decoder.Decode(&active)
	if err != nil {
		rt.logError(fmt.Errorf("ActivateBlockchain decode failed: %w", err))
		jsonresponse.RespondWithError(w, http.StatusBadRequest, err.Error())
		return
	}

	defer r.Body.Close()

	err = rt.Writer.ActivateBlockchain(blockchainID, active)
	if err != nil {
		rt.logError(fmt.Errorf("ActivateBlockchain failed: %w", err))
		jsonresponse.RespondWithError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonresponse.RespondWithJSON(w, http.StatusOK, active)
}

func (rt *Router) CreateBlockchain(w http.ResponseWriter, r *http.Request) {
	var blockchain repository.Blockchain

	decoder := json.NewDecoder(r.Body)

	err := decoder.Decode(&blockchain)
	if err != nil {
		rt.logError(fmt.Errorf("CreateBlockchain decode failed: %w", err))
		jsonresponse.RespondWithError(w, http.StatusBadRequest, err.Error())
		return
	}

	defer r.Body.Close()

	fullBlockchain, err := rt.Writer.WriteBlockchain(&blockchain)
	if err != nil {
		rt.logError(fmt.Errorf("WriteBlockchain in CreateBlockchain failed: %w", err))
		jsonresponse.RespondWithError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonresponse.RespondWithJSON(w, http.StatusOK, fullBlockchain)
}

func (rt *Router) GetBlockchains(w http.ResponseWriter, r *http.Request) {
	jsonresponse.RespondWithJSON(w, http.StatusOK, rt.Cache.GetBlockchains())
}

func (rt *Router) GetLoadBalancer(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	lb := rt.Cache.GetLoadBalancer(vars["id"])

	if lb == nil {
		rt.logError(fmt.Errorf("GetLoadBalancer failed: %w", errBalancerNotFound))
		jsonresponse.RespondWithError(w, http.StatusNotFound, errBalancerNotFound.Error())
		return
	}

	jsonresponse.RespondWithJSON(w, http.StatusOK, lb)
}

func (rt *Router) CreateLoadBalancer(w http.ResponseWriter, r *http.Request) {
	var lb repository.LoadBalancer

	decoder := json.NewDecoder(r.Body)

	err := decoder.Decode(&lb)
	if err != nil {
		rt.logError(fmt.Errorf("CreateLoadBalancer Decode failed: %w", err))
		jsonresponse.RespondWithError(w, http.StatusBadRequest, err.Error())
		return
	}

	defer r.Body.Close()

	fullLB, err := rt.Writer.WriteLoadBalancer(&lb)
	if err != nil {
		rt.logError(fmt.Errorf("WriteLoadBalancer in CreateLoadBalancer failed: %w", err))
		jsonresponse.RespondWithError(w, http.StatusInternalServerError, err.Error())
		return
	}

	for _, appID := range fullLB.ApplicationIDs {
		fullLB.Applications = append(fullLB.Applications, rt.Cache.GetApplication(appID))
	}

	fullLB.ApplicationIDs = nil // set to nil to avoid having two proofs of truth

	jsonresponse.RespondWithJSON(w, http.StatusOK, fullLB)
}

func (rt *Router) UpdateLoadBalancer(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	lb := rt.Cache.GetLoadBalancer(vars["id"])
	if lb == nil {
		rt.logError(fmt.Errorf("GetLoadBalancer in UpdateLoadBalancer failed: %w", errBalancerNotFound))
		jsonresponse.RespondWithError(w, http.StatusNotFound, errBalancerNotFound.Error())
		return
	}

	var updateInput repository.UpdateLoadBalancer

	decoder := json.NewDecoder(r.Body)

	err := decoder.Decode(&updateInput)
	if err != nil {
		jsonresponse.RespondWithError(w, http.StatusBadRequest, err.Error())
		return
	}

	defer r.Body.Close()

	if updateInput.Remove {
		err = rt.Writer.RemoveLoadBalancer(vars["id"])
		if err != nil {
			rt.logError(fmt.Errorf("RemoveLoadBalancer in UpdateLoadBalancer failed: %w", err))
			jsonresponse.RespondWithError(w, http.StatusInternalServerError, err.Error())
			return
		}

		lb.UserID = ""
	} else {
		err = rt.Writer.UpdateLoadBalancer(vars["id"], &updateInput)
		if err != nil {
			rt.logError(fmt.Errorf("UpdateLoadBalancer failed: %w", err))
			jsonresponse.RespondWithError(w, http.StatusInternalServerError, err.Error())
			return
		}

		if updateInput.Name != "" {
			lb.Name = updateInput.Name
		}
		if updateInput.StickyOptions != nil {
			lb.StickyOptions = *updateInput.StickyOptions
		}
	}

	jsonresponse.RespondWithJSON(w, http.StatusOK, lb)
}

func (rt *Router) GetLoadBalancers(w http.ResponseWriter, r *http.Request) {
	jsonresponse.RespondWithJSON(w, http.StatusOK, rt.Cache.GetLoadBalancers())
}

func (rt *Router) GetPayPlan(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	plan := rt.Cache.GetPayPlan(repository.PayPlanType(strings.ToUpper(vars["type"])))

	if plan == nil {
		rt.logError(fmt.Errorf("GetPayPlan failed: %w", errNoPayFound))
		jsonresponse.RespondWithError(w, http.StatusNotFound, errNoPayFound.Error())
		return
	}

	jsonresponse.RespondWithJSON(w, http.StatusOK, plan)
}

func (rt *Router) GetPayPlans(w http.ResponseWriter, r *http.Request) {
	jsonresponse.RespondWithJSON(w, http.StatusOK, rt.Cache.GetPayPlans())
}

func (rt *Router) CreateRedirect(w http.ResponseWriter, r *http.Request) {
	var redirect repository.Redirect

	decoder := json.NewDecoder(r.Body)

	err := decoder.Decode(&redirect)
	if err != nil {
		rt.logError(fmt.Errorf("CreateRedirect decode failed: %w", err))
		jsonresponse.RespondWithError(w, http.StatusBadRequest, err.Error())
		return
	}

	defer r.Body.Close()

	fullRedirect, err := rt.Writer.WriteRedirect(&redirect)
	if err != nil {
		rt.logError(fmt.Errorf("WriteRedirect in CreateRedirect failed: %w", err))
		jsonresponse.RespondWithError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonresponse.RespondWithJSON(w, http.StatusOK, fullRedirect)
}
