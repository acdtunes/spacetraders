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
