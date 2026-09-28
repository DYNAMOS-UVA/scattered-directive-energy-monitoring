package main

import (
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// sharedRulesResource is the path segment identifying the global Layer-2 rules
// update endpoint, as opposed to a steward name.
const sharedRulesResource = "sharedRules"

const policyEnforcerAckTimeout = 30 * time.Second

func archetypesHandler(etcdClient *clientv3.Client, root string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Info("Entering archetypesHandler")
		switch r.Method {
		case http.MethodGet:
			// Call your handler for GET
			api.GenericGetHandler[api.Archetype](w, r, etcdClient, "/archetypes")
		case http.MethodPut:
			// Call your handler for PUT
			archetype := &api.Archetype{}
			api.GenericPutToEtcd[api.Archetype](w, r, etcdClient, "/archetypes", archetype)
		default:
			// Respond with a 405 'Method Not Allowed' HTTP response if the method isn't supported
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func requestTypesHandler(etcdClient *clientv3.Client, etcdRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// Call your handler for GET
			api.GenericGetHandler[api.RequestType](w, r, etcdClient, etcdRoot)
		case http.MethodPut:
			// Call your handler for PUT
			requestType := &api.RequestType{}
			api.GenericPutToEtcd[api.RequestType](w, r, etcdClient, etcdRoot, requestType)
		default:
			// Respond with a 405 'Method Not Allowed' HTTP response if the method isn't supported
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func microserviceMetadataHandler(etcdClient *clientv3.Client, etcdRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// Call your handler for GET
			api.GenericGetHandler[api.MicroserviceMetadata](w, r, etcdClient, etcdRoot)
		case http.MethodPut:
			// Call your handler for PUT
			msMetadata := &api.MicroserviceMetadata{}
			api.GenericPutToEtcd[api.MicroserviceMetadata](w, r, etcdClient, etcdRoot, msMetadata)
		default:
			// Respond with a 405 'Method Not Allowed' HTTP response if the method isn't supported
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// agreementsHandler exposes the policy-update surface under /policyEnforcer.
//
//	GET  /policyEnforcer[/{steward}]  -> reads the legacy Agreement JSON objects.
//	PUT  /policyEnforcer/{steward}    -> per-steward policy update. Content-Type
//	                                     picks the format: application/json ->
//	                                     "json", anything else -> "eflint".
//	                                     Re-evaluates running jobs for {steward}.
//	PUT  /policyEnforcer/sharedRules  -> Layer-2 shared rules (always eFLINT text).
//	                                     Re-evaluates every running job.
func agreementsHandler(etcdClient *clientv3.Client, etcdRoot string) http.HandlerFunc {
	// The Agreement JSON objects live under a sub-prefix; the other sub-trees
	// (eflintModels, eflintRules, configs) hold content that cannot be
	// unmarshalled as api.Agreement.
	agreementsPrefix := etcdRoot + "/agreements/"

	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// GenericGetHandler uses its etcdRoot both as a URL-path prefix and
			// as the etcd key prefix, so rewrite the path to match
			// agreementsPrefix instead of passing it directly.
			r2 := r.Clone(r.Context())
			r2.URL.Path = agreementsPrefix + strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, etcdRoot), "/")
			api.GenericGetHandler[api.Agreement](w, r2, etcdClient, agreementsPrefix)
		case http.MethodPut:
			handlePutPolicyResource(w, r, etcdRoot)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// handlePutPolicyResource routes a PUT by the path suffix after etcdRoot:
// either a steward name or the reserved "sharedRules" resource.
func handlePutPolicyResource(w http.ResponseWriter, r *http.Request, etcdRoot string) {
	suffix := strings.Trim(strings.TrimPrefix(r.URL.Path, etcdRoot), "/")
	if suffix == "" {
		http.Error(w, "PUT requires a steward name or 'sharedRules' in the path", http.StatusBadRequest)
		return
	}
	if strings.Contains(suffix, "/") {
		http.Error(w, "nested resources are not supported", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		logger.Sugar().Errorf("Error reading body: %v", err)
		http.Error(w, "Error reading request body", http.StatusBadRequest)
		return
	}
	if len(body) == 0 {
		http.Error(w, "request body is empty", http.StatusBadRequest)
		return
	}

	if suffix == sharedRulesResource {
		handlePutSharedRules(w, r, body)
		return
	}

	handlePutStewardAgreement(w, r, suffix, body)
}

// handlePutStewardAgreement validates and persists one steward's policy via the
// policy enforcer, then re-evaluates that steward's running jobs.
func handlePutStewardAgreement(w http.ResponseWriter, r *http.Request, steward string, body []byte) {
	policyUpdate := &pb.PolicyUpdate{
		Type:             "agreementUpdate",
		AgreementName:    steward,
		AgreementPayload: body,
		Format:           formatFromContentType(r.Header.Get("Content-Type")),
	}

	if !submitPolicyUpdate(w, r, policyUpdate, "Policy update rejected by Policy Enforcer") {
		return
	}

	go checkJobs(steward)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// handlePutSharedRules validates and persists the consortium-wide Layer-2 rules,
// then re-evaluates every running job since shared rules affect all agreements.
func handlePutSharedRules(w http.ResponseWriter, r *http.Request, body []byte) {
	policyUpdate := &pb.PolicyUpdate{
		Type:             "sharedRulesUpdate",
		AgreementPayload: body,
	}

	if !submitPolicyUpdate(w, r, policyUpdate, "Shared rules update rejected by Policy Enforcer") {
		return
	}

	go checkAllJobs()
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// submitPolicyUpdate dispatches the update, waits for the enforcer's verdict and
// writes the appropriate HTTP error when it fails. It returns false if the
// caller should stop (an error response has already been written).
func submitPolicyUpdate(w http.ResponseWriter, r *http.Request, policyUpdate *pb.PolicyUpdate, rejectMessage string) bool {
	correlationId := uuid.New().String()
	policyUpdate.RequestMetadata = &pb.RequestMetadata{
		DestinationQueue: "policyEnforcer-in",
		CorrelationId:    correlationId,
	}

	res, ok := awaitPolicyEnforcerAck(w, r, policyUpdate, correlationId)
	if !ok {
		return false
	}

	if res.ValidationResponse != nil && !res.ValidationResponse.RequestApproved {
		http.Error(w, rejectMessage, http.StatusBadRequest)
		return false
	}

	return true
}

// awaitPolicyEnforcerAck submits the policy update and blocks until the matching
// ack (keyed by correlation id) arrives or the timeout fires. On failure it has
// already written an HTTP error to w and returns ok=false.
func awaitPolicyEnforcerAck(
	w http.ResponseWriter,
	r *http.Request,
	policyUpdate *pb.PolicyUpdate,
	correlationId string,
) (*pb.PolicyUpdate, bool) {
	resChan := make(chan *pb.PolicyUpdate, 1)
	agreementUpdateMutex.Lock()
	agreementUpdateMap[correlationId] = resChan
	agreementUpdateMutex.Unlock()

	defer func() {
		agreementUpdateMutex.Lock()
		delete(agreementUpdateMap, correlationId)
		agreementUpdateMutex.Unlock()
	}()

	logger.Sugar().Debugf("awaitPolicyEnforcerAck: sending type=%q correlationId=%s", policyUpdate.Type, correlationId)
	if _, err := c.SendPolicyUpdate(r.Context(), policyUpdate); err != nil {
		logger.Sugar().Errorf("error sending policy update: %v", err)
		http.Error(w, "Failed to dispatch policy update", http.StatusInternalServerError)
		return nil, false
	}

	select {
	case res := <-resChan:
		approved := res.ValidationResponse != nil && res.ValidationResponse.RequestApproved
		logger.Sugar().Debugf("awaitPolicyEnforcerAck: ack received correlationId=%s approved=%v", correlationId, approved)
		return res, true
	case <-time.After(policyEnforcerAckTimeout):
		logger.Sugar().Warnf("awaitPolicyEnforcerAck: timeout waiting for ack correlationId=%s", correlationId)
		http.Error(w, "Timeout waiting for Policy Enforcer validation", http.StatusGatewayTimeout)
		return nil, false
	}
}

// formatFromContentType maps the request Content-Type to the format label the
// policy enforcer understands. Only application/json selects the legacy JSON
// validator; every other media type selects eFLINT.
func formatFromContentType(contentType string) string {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType == "" {
		return api.ValidationStrategyEflint
	}
	if strings.EqualFold(mediaType, "application/json") {
		return "json"
	}
	return api.ValidationStrategyEflint
}

func updateEtc() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Your original updateEtc code here.
		go registerPolicyEnforcerConfiguration()

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Updated all config"))
	}
}
