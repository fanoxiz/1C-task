package protocol

import (
	"encoding/json"
	"testing"
)

func TestMoveRequestRequiresCoordinates(t *testing.T) {
	for _, body := range []string{`{"request_id":"id","expected_move_number":1,"row":0}`, `{"request_id":"id","expected_move_number":1,"row":null,"column":0}`, `{"row":0,"column":0}`, `{"expected_move_number":1,"row":0,"column":0,"extra":true}`} {
		var request MoveRequest
		if err := json.Unmarshal([]byte(body), &request); err == nil {
			t.Fatalf("accepted incomplete or ambiguous move: %s", body)
		}
	}
	var request MoveRequest
	if err := json.Unmarshal([]byte(`{"request_id":"00000000-0000-0000-0000-000000000001","expected_move_number":1,"row":0,"column":0}`), &request); err != nil || !request.Valid() {
		t.Fatalf("valid zero coordinates rejected: %+v %v", request, err)
	}
}

func TestUUIDRejectsExtraSeparators(t *testing.T) {
	if !IsUUID("00000000-0000-0000-0000-000000000001") || IsUUID("00000000-0000-0000-0000-00000000--01") {
		t.Fatal("invalid UUID validation")
	}
}
