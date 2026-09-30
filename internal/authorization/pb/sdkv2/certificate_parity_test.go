package v2pb_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"

	sdkv2 "spaghetti/internal/authorization/pb/sdkv2"
	v2pb "spaghetti/internal/authorization/pb/v2"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestSDKV2CertificateSchemaParity(t *testing.T) {
	compressed, _ := (&sdkv2.AuthorizationCertificateV2{}).Descriptor()
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var sdkFile descriptorpb.FileDescriptorProto
	if err := proto.Unmarshal(data, &sdkFile); err != nil {
		t.Fatal(err)
	}
	canonicalFile := protodesc.ToFileDescriptorProto((&v2pb.AuthorizationCertificateV2{}).ProtoReflect().Descriptor().ParentFile())
	if sdkFile.GetPackage() != canonicalFile.GetPackage() || len(sdkFile.MessageType) != len(canonicalFile.MessageType) {
		t.Fatal("SDK V2 package or message count differs from canonical schema")
	}
	for i := range sdkFile.MessageType {
		if !proto.Equal(sdkFile.MessageType[i], canonicalFile.MessageType[i]) {
			t.Fatalf("SDK V2 message %d differs from canonical schema", i)
		}
	}
}
