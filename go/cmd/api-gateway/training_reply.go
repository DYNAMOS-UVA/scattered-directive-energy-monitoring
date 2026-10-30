package main

import (
	"fmt"
	"math"
	"strings"

	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func validateTrainingReply(requestType, participant string, reply *pb.MicroserviceCommunication) error {
	required := map[string]string{}
	switch requestType {
	case "vflInitializeRequest":
		switch strings.ToLower(participant) {
		case Authority:
			required = map[string]string{"mife_encryption_keys": "list", "sife_public_key": "string", "mife_public_key": "string"}
		case Aggregator:
			required["weights"] = "list"
		default:
			required["features_size"] = "number"
		}
	case "vflSampleBatchRequest":
		required["sample_batch_indexes"] = "string"
	case "vflExtractCiphertextsRequest":
		required["feature_dimension"] = "string"
		if !strings.EqualFold(participant, Server) {
			required["sample_dimension"] = "list"
		}
	case "vflMIFEDKGenerationRequest":
		required["dks_features_mife"] = "list"
	case "vflFeaturesDecryptionRequest":
		required["decrypted_features_dimension"] = "list"
	case "vflCalculateAccuracyRequest":
		required = map[string]string{"batch_accuracy": "number", "logistic_error": "list"}
	case "vflSIFEDKGenerationRequest":
		required["dk_samples_sife"] = "string"
	case "vflSamplesDecryptionRequest":
		required["weights"] = "list"
	}
	for name, kind := range required {
		value := reply.GetData().GetFields()[name]
		valid := false
		switch kind {
		case "list":
			valid = len(value.GetListValue().GetValues()) > 0
		case "string":
			valid = value.GetStringValue() != ""
		case "number":
			_, valid = value.GetKind().(*structpb.Value_NumberValue)
			valid = valid && !math.IsNaN(value.GetNumberValue()) && !math.IsInf(value.GetNumberValue(), 0)
		}
		if !valid {
			return fmt.Errorf("%s reply from %s: missing or invalid %s", requestType, participant, name)
		}
	}
	if requestType == "vflCalculateAccuracyRequest" {
		accuracy := reply.Data.Fields["batch_accuracy"].GetNumberValue()
		if accuracy < 0 || accuracy > 1 {
			return fmt.Errorf("batch accuracy %v is outside [0,1]", accuracy)
		}
		if requestType == "vflInitializeRequest" && !strings.EqualFold(participant, Authority) && !strings.EqualFold(participant, Aggregator) && !strings.EqualFold(participant, Server) {
			size := reply.Data.Fields["features_size"].GetNumberValue()
			if size <= 0 || math.Trunc(size) != size {
				return fmt.Errorf("invalid feature count %v for %s", size, participant)
			}
		}
	}
	return nil
}
