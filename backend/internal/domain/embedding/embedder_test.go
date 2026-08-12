package embedding

import "testing"

func TestEmbedRequestKeepsInputOrder(t *testing.T) {
	request := EmbedRequest{
		Inputs:     []string{"first chunk", "second chunk"},
		Model:      "test-model",
		Dimensions: 2,
	}

	if request.Inputs[0] != "first chunk" || request.Inputs[1] != "second chunk" {
		t.Fatalf("EmbedRequest inputs = %v, want original order", request.Inputs)
	}
}
