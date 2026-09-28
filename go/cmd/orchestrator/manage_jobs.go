package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"github.com/Jorrit05/DYNAMOS/pkg/etcd"
	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"github.com/google/uuid"
)

const revocationDeletesJobsEnv = "POLICY_REVOCATION_DELETE_JOBS"

// Deleting job registrations on revocation is opt-in. While stewards exist that
// the eFLINT reasoner cannot evaluate, an empty ValidDataproviders usually means
// "could not evaluate" rather than "access revoked", and deleting would orphan
// running jobs irrecoverably.
var revocationDeletesJobs = strings.EqualFold(os.Getenv(revocationDeletesJobsEnv), "true")

// /agents/jobs/SURF/jorrit.stutterheim@cloudnation.nl/jorrit-stutterheim-43ea82da
// {"archetype_id":"dataThroughTtp","request_type":"sqlDataRequest","role":"computeProvider","user":{"id":"12324","user_name":"jorrit.stutterheim@cloudnation.nl"},"data_providers":["UVA"],"destination_queue":"SURF-in","job_name":"jorrit-stutterheim-43ea82da","local_job_name":"jorrit-stutterheim-43ea82dasurf1"}
// /agents/jobs/SURF/queueInfo/jorrit-stutterheim-43ea82dasurf1
// jorrit-stutterheim-43ea82dasurf1
// /agents/jobs/UVA/jorrit.stutterheim@cloudnation.nl/jorrit-stutterheim-43ea82da
// {"archetype_id":"dataThroughTtp","request_type":"sqlDataRequest","role":"dataProvider","user":{"id":"12324","user_name":"jorrit.stutterheim@cloudnation.nl"},"destination_queue":"UVA-in","job_name":"jorrit-stutterheim-43ea82da","local_job_name":"jorrit-stutterheim-43ea82dauva1"}
// /agents/jobs/UVA/queueInfo/jorrit-stutterheim-43ea82dauva1
// jorrit-stutterheim-43ea82dauva1

// checkAllJobs re-evaluates running jobs for every steward that has at least
// one /agents/jobs/<steward>/... entry. Used after a shared-rules update, which
// affects derivations for every agreement.
func checkAllJobs() {
	rootKey := "/agents/jobs/"
	keys, err := etcd.GetFullKeysFromPrefix(etcdClient, rootKey, etcd.WithMaxElapsedTime(2*time.Second))
	if err != nil {
		logger.Sugar().Warnf("error listing job keys for global re-evaluation: %v", err)
		return
	}

	stewards := make(map[string]struct{})
	for _, k := range keys {
		trimmed := strings.TrimPrefix(k, rootKey)
		if trimmed == "" {
			continue
		}
		stewards[strings.SplitN(trimmed, "/", 2)[0]] = struct{}{}
	}

	if len(stewards) == 0 {
		logger.Debug("no active stewards with running jobs; nothing to re-evaluate")
		return
	}

	for steward := range stewards {
		checkJobs(steward)
	}
}

// checkJobs re-evaluates the running jobs of a single steward. The set of users
// is derived from the etcd job keys rather than from an agreement struct, since
// an eFLINT policy carries no machine-readable relation list.
func checkJobs(agreementName string) {
	key := fmt.Sprintf("/agents/jobs/%s/", agreementName)
	jobKeys, err := etcd.GetFullKeysFromPrefix(etcdClient, key, etcd.WithMaxElapsedTime(2*time.Second))
	if err != nil {
		logger.Sugar().Warnf("error get jobs: %v", err)
		return
	}

	// Key layout: /agents/jobs/<steward>/<user>/<job> -> parts[4] and parts[5].
	userJobs := make(map[string][]string)
	for _, k := range jobKeys {
		parts := strings.Split(k, "/")
		if len(parts) < 6 {
			continue
		}
		userName := parts[4]
		if userName == "queueInfo" {
			continue
		}
		userJobs[userName] = append(userJobs[userName], parts[5])
	}

	logger.Sugar().Debugf("checkJobs: agreement=%q found %d user(s) with active jobs", agreementName, len(userJobs))
	for userName, jobNames := range userJobs {
		logger.Sugar().Debugf("checkJobs: user=%q jobs(%d)=%v", userName, len(jobNames), jobNames)
		evaluateArchetypeInActiveJobs(jobNames, agreementName, userName, c)
	}
}

func evaluateArchetypeInActiveJobs(jobNames []string, agreementName string, relationName string, c pb.RabbitMQClient) {
	logger.Debug("starting evaluateArchetypeInActiveJobs")
	ctx := context.Background()
	// alue.ArchetypeId == archetype in current active job from the agreement name.

	// for each job. Check current archetype. versus new archetypes.
	for _, job := range jobNames {

		jobInfoKey := fmt.Sprintf("/agents/jobs/%s/%s/%s", agreementName, relationName, job)

		resp, err := etcdClient.Get(ctx, jobInfoKey)
		if err != nil {
			logger.Sugar().Errorf("error getting value from etcd: %v", err)
			continue
		}

		if len(resp.Kvs) == 0 {
			logger.Warn("this should not happen")
			continue
		}

		currentRegisteredJob := &pb.CompositionRequest{}
		err = json.Unmarshal(resp.Kvs[0].Value, currentRegisteredJob)
		if err != nil {
			logger.Sugar().Errorf("error unmarshalling jobinfo: %v", err)
		}

		policyUpdate := &pb.PolicyUpdate{
			Type:            "policyUpdate",
			User:            &pb.User{Id: relationName, UserName: relationName},
			RequestMetadata: &pb.RequestMetadata{DestinationQueue: "policyEnforcer-in"},
		}

		correlationId := uuid.New().String()
		policyUpdate.RequestMetadata.CorrelationId = correlationId

		agentsWithThisJob := make(map[string]*pb.CompositionRequest)

		ctx = getJobAcrossAgents(ctx, agentsWithThisJob, job, relationName)

		for k, v := range agentsWithThisJob {
			if v.Role == "all" || v.Role == "dataProvider" {
				policyUpdate.DataProviders = append(policyUpdate.DataProviders, k)
			}
		}

		policyUpdateMutex.Lock()
		policyUpdateMap[policyUpdate.RequestMetadata.CorrelationId] = agentsWithThisJob
		policyUpdateMutex.Unlock()
		c.SendPolicyUpdate(ctx, policyUpdate)
	}
}

// deleteJobAcrossAgents removes every etcd entry (job info + queueInfo) for the
// given job across all agents that were holding it.
func deleteJobAcrossAgents(ctx context.Context, agentsWithThisJob map[string]*pb.CompositionRequest, userName string) {
	for agent, jobData := range agentsWithThisJob {
		jobKey := fmt.Sprintf("/agents/jobs/%s/%s/%s", agent, userName, jobData.JobName)
		if _, err := etcdClient.Delete(ctx, jobKey); err != nil {
			logger.Sugar().Warnf("deleteJobAcrossAgents: error deleting job key %s: %v", jobKey, err)
		}

		queueInfoKey := fmt.Sprintf("/agents/jobs/%s/queueInfo/%s", agent, jobData.LocalJobName)
		if _, err := etcdClient.Delete(ctx, queueInfoKey); err != nil {
			logger.Sugar().Warnf("deleteJobAcrossAgents: error deleting queueInfo key %s: %v", queueInfoKey, err)
		}
	}
}

func processPolicyUpdate(ctx context.Context, agentsWithThisJob map[string]*pb.CompositionRequest, policyUpdate *pb.PolicyUpdate) {
	logger.Sugar().Debugf("processPolicyUpdate")

	// Authorisation fully revoked: there is nothing left to route to, so every
	// running job for this user must be cleaned up.
	vr := policyUpdate.ValidationResponse
	if vr == nil || len(vr.ValidDataproviders) == 0 {
		if !revocationDeletesJobs {
			logger.Sugar().Warnf("processPolicyUpdate: no valid data providers for user %q; leaving %d job registration(s) untouched (set %s=true to enable deletion)",
				policyUpdate.User.UserName, len(agentsWithThisJob), revocationDeletesJobsEnv)
			return
		}
		logger.Sugar().Infof("processPolicyUpdate: no valid data providers — deleting all active jobs for user %q", policyUpdate.User.UserName)
		deleteJobAcrossAgents(ctx, agentsWithThisJob, policyUpdate.User.UserName)
		return
	}

	// TODO: Kinda threw this in without testing..
	authorizedProviders, err := getAuthorizedProviders(policyUpdate.ValidationResponse)
	if err != nil {
		logger.Sugar().Errorf("error getAuthorizedProviders : %v", err)
	}

	archetype, err := chooseArchetype(policyUpdate.ValidationResponse, authorizedProviders)
	if err != nil {
		logger.Sugar().Errorf("error choosing archetype: %v", err)
	}

	logger.Sugar().Debugf("New archetype: %v", archetype)

	var archetypeConfig api.Archetype
	_, err = etcd.GetAndUnmarshalJSON(etcdClient, fmt.Sprintf("/archetypes/%s", archetype), &archetypeConfig)
	if err != nil {
		logger.Sugar().Errorf("error choosing archetype: %v", err)
		return
	}

	// technically now, this shouldn't be necessary
	computeProviderAlready := false
	var ttp lib.AgentDetails
	for agent, currentData := range agentsWithThisJob {
		if currentData.ArchetypeId == archetype {
			logger.Sugar().Debug("same archetype, do nothing")
			return
		}
		key := fmt.Sprintf("/agents/jobs/%s/%s/%s", agent, policyUpdate.User.UserName, currentData.JobName)

		// TODO: If compute provider is "clients" for VFL
		if archetypeConfig.ComputeProvider != "other" {
			if currentData.Role == "computeProvider" {
				// Delete this job info
				_, err := etcdClient.Delete(ctx, key)
				if err != nil {
					logger.Sugar().Warnf("error deleting key from etcd: %v", err)
				}
				continue
			}

			// New archetype is computeToData
			newData := currentData
			newData.ArchetypeId = archetype
			newData.Role = "all"
			newData.DataProviders = []string{}
			err := etcd.SaveStructToEtcd(etcdClient, key, newData)
			if err != nil {
				logger.Sugar().Errorf("Error saving struct to etcd: %v", err)
				return
			}
			computeProviderAlready = true
		} else {
			var err error
			ttp, err = chooseThirdParty(policyUpdate.ValidationResponse)
			if err != nil {
				logger.Sugar().Errorf("Error choosing third party: %v", err)
				return
			}

			if currentData.Role == "computeProvider" && agent == ttp.Name {
				computeProviderAlready = true
				continue
			} else if currentData.Role == "computeProvider" && agent != ttp.Name {
				// Delete this job info
				_, err := etcdClient.Delete(ctx, key)
				if err != nil {
					logger.Sugar().Warnf("error deleting key from etcd: %v", err)
				}
				continue
			}

			if currentData.Role == "all" {
				_, ok := policyUpdate.ValidationResponse.ValidDataproviders[agent]
				if !ok {
					// Delete this job info
					_, err := etcdClient.Delete(ctx, key)
					if err != nil {
						logger.Sugar().Warnf("error deleting key from etcd: %v", err)
					}
				}

				// New archetype is dataThroughTtp
				newData := currentData
				newData.ArchetypeId = archetype
				newData.Role = "dataProvider"
				newData.DataProviders = []string{}

				err = etcd.SaveStructToEtcd(etcdClient, key, newData)
				if err != nil {
					logger.Sugar().Errorf("Error saving struct to etcd: %v", err)
					return
				}

			}

		}
	}

	if !computeProviderAlready {
		compositionRequest := &pb.CompositionRequest{}
		compositionRequest.User = policyUpdate.User
		tmpDataProvider := []string{}

		for key := range policyUpdate.ValidationResponse.ValidDataproviders {
			tmpDataProvider = append(tmpDataProvider, key)
		}
		compositionRequest.Role = "computeProvider"
		compositionRequest.DataProviders = tmpDataProvider
		compositionRequest.ArchetypeId = archetype
		for _, v := range agentsWithThisJob {
			compositionRequest.RequestType = v.RequestType
			compositionRequest.JobName = v.JobName
			break
		}

		compositionRequest.DestinationQueue = ttp.RoutingKey

		c.SendCompositionRequest(ctx, compositionRequest)
	}
}

func getJobAcrossAgents(ctx context.Context, targetMap map[string]*pb.CompositionRequest, jobName string, userName string) context.Context {

	var agents *lib.AgentDetails
	key := "/agents/online/"
	activeAgents, err := etcd.GetPrefixListEtcd(etcdClient, key, agents)
	if err != nil {
		logger.Sugar().Warnf("error get agents: %v", err)
	}

	for _, agent := range activeAgents {

		key := fmt.Sprintf("/agents/jobs/%s/%s/%s", agent.Name, userName, jobName)

		resp, err := etcdClient.Get(ctx, key)
		if err != nil {
			logger.Sugar().Errorf("error getting value from etcd: %v", err)
			continue
		}

		if len(resp.Kvs) == 0 {
			logger.Sugar().Debugw("no value found for", "key", key)
			continue
		}

		agentsConfiguration := &pb.CompositionRequest{}
		err = json.Unmarshal(resp.Kvs[0].Value, agentsConfiguration)
		if err != nil {
			logger.Sugar().Errorf("error unmarshalling jobinfo: %v", err)
			continue
		}

		targetMap[agent.Name] = agentsConfiguration
	}

	return ctx
}

func handleRequestApproval(ctx context.Context, validationResponse *pb.ValidationResponse, redeploy bool) {
	result := &pb.RequestApprovalResponse{Type: "requestApprovalResponse", RequestMetadata: &pb.RequestMetadata{DestinationQueue: "api-gateway-in"}}

	authorizedProviders, err := getAuthorizedProviders(validationResponse)
	if err != nil {
		result.Error = err.Error()
		c.SendRequestApprovalResponse(ctx, result)
		return
	}

	if len(authorizedProviders) == 0 {
		// TODO Respond with the following to the rabbitmq queue
		// []byte("Request was processed, but no agreements or available dataproviders have been found")
		result.Error = "Request was processed, but no agreements or available dataproviders have been found"
		c.SendRequestApprovalResponse(ctx, result)
		return
	}

	// TODO: Might be able to improve processing by converting functions to go routines
	// Seems a bit tricky though due to the response writer.

	compositionRequest := &pb.CompositionRequest{}
	compositionRequest.User = &pb.User{}
	userTargets, ctx, err := startCompositionRequest(ctx, validationResponse, authorizedProviders, compositionRequest, redeploy)
	if err != nil {
		switch e := err.(type) {
		case *UnauthorizedProviderError:
			logger.Sugar().Warn("Unauthorized provider error: %v", e)
			return
		default:
			logger.Sugar().Errorf("Error starting composition request: %v", err)
			return
		}
	}

	result.Auth = &pb.Auth{}
	result.User = &pb.User{}

	result.Auth = validationResponse.Auth
	result.User = validationResponse.User

	result.AuthorizedProviders = make(map[string]string)
	result.AuthorizedProviders = userTargets
	result.JobId = compositionRequest.JobName

	c.SendRequestApprovalResponse(ctx, result)
}
