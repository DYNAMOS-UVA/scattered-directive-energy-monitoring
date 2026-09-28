package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"github.com/Jorrit05/DYNAMOS/pkg/etcd"
	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
)

func registerPolicyEnforcerConfiguration() {
	logger.Debug("Start registerPolicyEnforcerConfiguration")
	// Load request types
	var requestsTypes []api.RequestType
	lib.UnmarshalJsonFile(requestTypeConfigLocation, &requestsTypes)

	for _, requestType := range requestsTypes {
		etcd.SaveStructToEtcd(etcdClient, fmt.Sprintf("/requestTypes/%s", requestType.Name), requestType)
	}

	// Load archetypes
	var archeTypes []api.Archetype
	lib.UnmarshalJsonFile(archetypeConfigLocation, &archeTypes)

	for _, archeType := range archeTypes {
		etcd.SaveStructToEtcd(etcdClient, fmt.Sprintf("/archetypes/%s", archeType.Name), archeType)
	}

	// Load labels and allowedOutputs (microservice.json)
	var microservices []api.MicroserviceMetadata

	lib.UnmarshalJsonFile(microserviceMetadataConfigLocation, &microservices)

	for _, microservice := range microservices {
		etcd.SaveStructToEtcd(etcdClient, fmt.Sprintf("/microservices/%s/chainMetadata", microservice.Name), microservice)
	}

	// Load agreemnents  (agreemnents.json)
	var agreements []api.Agreement

	lib.UnmarshalJsonFile(agreementsConfigLocation, &agreements)

	for _, agreement := range agreements {
		etcd.SaveStructToEtcd(etcdClient, fmt.Sprintf("/policyEnforcer/agreements/%s", agreement.Name), agreement)
	}

	// Load agreemnents  (agreemnents.json)
	var datasets []*pb.Dataset

	lib.UnmarshalJsonFile(dataSetConfigLocation, &datasets)

	for _, dataset := range datasets {
		etcd.SaveStructToEtcd(etcdClient, fmt.Sprintf("/datasets/%s", dataset.Name), dataset)
	}

	// Load   optional_microservices.json
	var optionalServices []api.OptionalServices

	lib.UnmarshalJsonFile(optionalMSConfigLocation, &optionalServices)

	for _, services := range optionalServices {
		for k, msList := range services.Types {
			for _, ms := range msList {
				key := fmt.Sprintf("/agents/%s/requestType/%s/%s ", services.DataSteward, k, ms)
				etcd.PutValueToEtcd(etcdClient, key, ms)
			}
		}
	}

	registerEflintSpecifications()

	// Load provider_configs.json, which selects the validation strategy
	// (legacy JSON vs eFLINT) per data steward. Stewards without an entry
	// default to legacy inside the policy enforcer.
	var providerConfigs []api.ProviderValidationConfig

	lib.UnmarshalJsonFile(providerConfigsLocation, &providerConfigs)

	for _, config := range providerConfigs {
		etcd.SaveStructToEtcd(etcdClient, fmt.Sprintf("/policyEnforcer/configs/%s", config.Name), config)
	}
}

// registerEflintSpecifications publishes the eFLINT layer files staged on the
// etcd PVC into the keys the policy enforcer reads.
//
// File-name routing convention:
//
//	01_interface_policy.eflint -> /policyEnforcer/eflintLayer1/interface
//	                              (informational; the enforcer embeds Layer 1)
//	02_agreement_rules.eflint  -> /policyEnforcer/eflintRules/shared
//	<steward>.eflint           -> /policyEnforcer/eflintModels/<steward>
func registerEflintSpecifications() {
	logger.Sugar().Debugf("Loading eFLINT models from directory %s", eflintModelsDirectory)

	entries, err := os.ReadDir(eflintModelsDirectory)
	if err != nil {
		logger.Sugar().Errorf("Failed to read eFLINT models directory %s: %v", eflintModelsDirectory, err)
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".eflint" {
			continue
		}

		filePath := filepath.Join(eflintModelsDirectory, entry.Name())
		content, err := os.ReadFile(filePath)
		if err != nil {
			logger.Sugar().Errorf("Failed to read eFLINT model file %s: %v", entry.Name(), err)
			continue
		}

		modelName := strings.TrimSuffix(entry.Name(), ".eflint")

		var key string
		switch modelName {
		case "01_interface_policy":
			key = "/policyEnforcer/eflintLayer1/interface"
		case "02_agreement_rules":
			key = "/policyEnforcer/eflintRules/shared"
		default:
			key = fmt.Sprintf("/policyEnforcer/eflintModels/%s", modelName)
		}

		etcd.PutValueToEtcd(etcdClient, key, string(content))
		logger.Sugar().Debugf("Loaded eFLINT spec %s into %s", modelName, key)
	}
}
