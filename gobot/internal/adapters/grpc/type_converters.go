package grpc

// PlayerID conversion helpers for domain <-> protobuf boundary

// ToProtobufPlayerID converts domain int to protobuf int32
func ToProtobufPlayerID(domainID int) int32 {
	return int32(domainID)
}

// FromProtobufPlayerID converts protobuf int32 to domain int
func FromProtobufPlayerID(protoID int32) int {
	return int(protoID)
}

func stringValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// toInterfaceSlice widens a string slice for a container config map, whose values are
// round-tripped through JSON as interface{}.
func toInterfaceSlice(values []string) []interface{} {
	out := make([]interface{}, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}
