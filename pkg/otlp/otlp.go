// Copyright (c) 2021-2024 SigScalr, Inc.
//
// This file is part of SigLens Observability Solution
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package otlp

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/siglens/siglens/pkg/config"
	"github.com/siglens/siglens/pkg/es/writer"
	"github.com/siglens/siglens/pkg/grpc"
	"github.com/siglens/siglens/pkg/hooks"
	segwriter "github.com/siglens/siglens/pkg/segment/writer"
	"github.com/siglens/siglens/pkg/usageStats"
	"github.com/siglens/siglens/pkg/utils"
	log "github.com/sirupsen/logrus"
	"github.com/valyala/fasthttp"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/proto"
)

func ProcessTraceIngest(ctx *fasthttp.RequestCtx) {
	if hook := hooks.GlobalHooks.OverrideIngestRequestHook; hook != nil {
		alreadyHandled := hook(ctx, 0 /* TODO */, grpc.INGEST_FUNC_OTLP_TRACES, false)
		if alreadyHandled {
			return
		}
	}

	// All requests and responses should be protobufs.
	ctx.Response.Header.Set("Content-Type", "application/x-protobuf")
	if string(ctx.Request.Header.Peek("Content-Type")) != "application/x-protobuf" {
		log.Infof("ProcessTraceIngest: got a non-protobuf request. Got Content-Type: %s", string(ctx.Request.Header.Peek("Content-Type")))
		setFailureResponse(ctx, fasthttp.StatusBadRequest, "Expected a protobuf request")
		return
	}

	// Get the data from the request.
	data := ctx.PostBody()
	if requiresGzipDecompression(ctx) {
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			setFailureResponse(ctx, fasthttp.StatusBadRequest, "Unable to gzip decompress the data")
			return
		}

		data, err = io.ReadAll(reader)
		if err != nil {
			setFailureResponse(ctx, fasthttp.StatusBadRequest, "Unable to gzip decompress the data")
			return
		}
	}

	// Unmarshal the data.
	request, err := unmarshalTraceRequest(data)
	if err != nil {
		log.Errorf("ProcessTraceIngest: failed to unpack Data: %s with err %v", string(data), err)
		setFailureResponse(ctx, fasthttp.StatusBadRequest, "Unable to unmarshal traces")
		return
	}

	// Setup ingestion parameters.
	now := utils.GetCurrentTimeInMs()
	indexName := "traces"
	shouldFlush := false
	localIndexMap := make(map[string]string)
	orgId := uint64(0)
	tsKey := config.GetTimeStampKey()

	idxToStreamIdCache := make(map[string]string)
	cnameCacheByteHashToStr := make(map[uint64]string)
	var jsParsingStackbuf [utils.UnescapeStackBufSize]byte

	// Go through the request data and ingest each of the spans.
	numSpans := 0       // The total number of spans sent in this request.
	numFailedSpans := 0 // The number of spans that we could not ingest.
	for _, resourceSpans := range request.ResourceSpans {
		// Find the service name.
		var service string
		if resourceSpans.Resource != nil {
			for _, keyvalue := range resourceSpans.Resource.Attributes {
				if keyvalue.Key == "service.name" {
					service = keyvalue.Value.GetStringValue()
				}
			}
		}
		pleArray := make([]*segwriter.ParsedLogEvent, 0)
		// Ingest each of these spans.
		for _, scopeSpans := range resourceSpans.ScopeSpans {
			numSpans += len(scopeSpans.Spans)
			for _, span := range scopeSpans.Spans {
				jsonData, err := spanToJson(span, service)
				if err != nil {
					log.Errorf("ProcessTraceIngest: failed to marshal span %s: %v. Service name: %s", span, err, service)
					numFailedSpans++
					continue
				}
				
				ple, err := writer.GetNewPLE(jsonData, now, indexName, &tsKey, jsParsingStackbuf[:])
				if err != nil {
					log.Errorf("ProcessTraceIngest: failed to get new PLE, jsonData: %v, err: %v", jsonData, err)
					numFailedSpans++
					continue
				}
				pleArray = append(pleArray, ple)
			}
		}
		err = writer.ProcessIndexRequestPle(now, indexName, shouldFlush, localIndexMap, orgId, 0, idxToStreamIdCache, cnameCacheByteHashToStr, jsParsingStackbuf[:], pleArray)
		if err != nil {
			log.Errorf("ProcessTraceIngest: Failed to ingest traces, err: %v", err)
			numFailedSpans += len(pleArray)
		}
	}

	log.Debugf("ProcessTraceIngest: %v spans in the request and failed to ingest %v of them", numSpans, numFailedSpans)
	usageStats.UpdateTracesStats(uint64(len(data)), uint64(numSpans), 0)
	// Send the appropriate response.
	handleTraceIngestionResponse(ctx, numSpans, numFailedSpans)
}

func requiresGzipDecompression(ctx *fasthttp.RequestCtx) bool {
	encoding := string(ctx.Request.Header.Peek("Content-Encoding"))
	if encoding == "gzip" {
		return true
	}

	if encoding != "" && encoding != "none" {
		log.Errorf("requiresGzipDecompression: invalid content encoding: %s. Request headers: %v", encoding, ctx.Request.Header.String())
	}

	return false
}

func unmarshalTraceRequest(data []byte) (*coltracepb.ExportTraceServiceRequest, error) {
	var trace coltracepb.ExportTraceServiceRequest
	err := proto.Unmarshal(data, &trace)
	if err != nil {
		log.Errorf("unmarshalTraceRequest: failed to unmarshal trace request. err: %v data: %v", err, string(data))
		return nil, err
	}
	return &trace, nil
}

func spanToJson(span *tracepb.Span, service string) ([]byte, error) {
	result := make(map[string]interface{})
	result["trace_id"] = hex.EncodeToString(span.TraceId)
	result["span_id"] = hex.EncodeToString(span.SpanId)
	result["parent_span_id"] = hex.EncodeToString(span.ParentSpanId)
	result["service"] = service
	result["trace_state"] = span.TraceState
	result["name"] = span.Name
	result["kind"] = span.Kind.String()
	result["start_time"] = span.StartTimeUnixNano
	result["end_time"] = span.EndTimeUnixNano
	result["duration"] = span.EndTimeUnixNano - span.StartTimeUnixNano
	result["dropped_attributes_count"] = uint64(span.DroppedAttributesCount)
	result["dropped_events_count"] = uint64(span.DroppedEventsCount)
	result["dropped_links_count"] = uint64(span.DroppedLinksCount)
	if span.Status != nil {
		result["status"] = span.Status.Code.String()
	} else {
		result["status"] = "Unknown"
	}

	// Make a column for each attribute key.
	for _, keyvalue := range span.Attributes {
		key, value, err := extractKeyValue(keyvalue)
		if err != nil {
			return nil, fmt.Errorf("spanToJson: failed to extract KeyValue: %v", err)
		}

		result[key] = value
	}

	eventsJson, err := json.Marshal(span.Events)
	if err != nil {
		log.Errorf("spanToJson: failed to marshal span with ID %s: %v", hex.EncodeToString(span.SpanId), err)
		return nil, err
	}
	result["events"] = string(eventsJson)

	linksJson, err := linksToJson(span.Links)
	if err != nil {
		return nil, err
	}
	result["links"] = string(linksJson)

	bytes, err := json.Marshal(result)
	return bytes, err
}

func extractKeyValue(keyvalue *commonpb.KeyValue) (string, interface{}, error) {
	value, err := extractAnyValue(keyvalue.Value)
	if err != nil {
		log.Errorf("extractKeyValue: failed to extract value for key %s: %v", keyvalue.Key, err)
		return "", nil, err
	}

	return keyvalue.Key, value, nil
}

func extractAnyValue(anyValue *commonpb.AnyValue) (interface{}, error) {
	switch anyValue.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return anyValue.GetStringValue(), nil
	case *commonpb.AnyValue_IntValue:
		return anyValue.GetIntValue(), nil
	case *commonpb.AnyValue_DoubleValue:
		return anyValue.GetDoubleValue(), nil
	case *commonpb.AnyValue_BoolValue:
		return anyValue.GetBoolValue(), nil
	case *commonpb.AnyValue_ArrayValue:
		arrayValue := anyValue.GetArrayValue().Values
		value := make([]interface{}, len(arrayValue))
		for i := range arrayValue {
			var err error
			value[i], err = extractAnyValue(arrayValue[i])
			if err != nil {
				return nil, err
			}
		}

		return value, nil
	default:
		return nil, fmt.Errorf("extractAnyValue: unsupported value type: %T", anyValue.Value)
	}
}

func linksToJson(spanLinks []*tracepb.Span_Link) ([]byte, error) {
	// Links have SpanId and TraceId fields that we want to display has hex, so
	// we need custom JSON marshalling.
	type Link struct {
		TraceId    string                 `json:"trace_id,omitempty"`
		SpanId     string                 `json:"span_id,omitempty"`
		TraceState string                 `json:"trace_state,omitempty"`
		Attributes map[string]interface{} `json:"attributes,omitempty"`
	}
	links := make([]Link, len(spanLinks))

	for i, link := range spanLinks {
		attributes := make(map[string]interface{})
		for _, keyvalue := range link.Attributes {
			key, value, err := extractKeyValue(keyvalue)
			if err != nil {
				log.Errorf("linksToJson: failed to extract link attribute for key-value pair %v. err: %v", keyvalue, err)
				return nil, err
			}

			attributes[key] = value
		}

		links[i] = Link{
			TraceId:    string(link.TraceId),
			SpanId:     string(link.SpanId),
			TraceState: link.TraceState,
			Attributes: attributes,
		}
	}

	jsonLinks, err := json.Marshal(links)
	if err != nil {
		log.Errorf("linksToJson: failed to marshal links to JSON. links: %v err: %v", links, err)
		return nil, err
	}

	return jsonLinks, nil
}

func setFailureResponse(ctx *fasthttp.RequestCtx, statusCode int, message string) {
	ctx.SetStatusCode(statusCode)

	failureStatus := status.Status{
		Code:    int32(statusCode),
		Message: message,
	}

	bytes, err := proto.Marshal(&failureStatus)
	if err != nil {
		log.Errorf("setFailureResponse: failed to marshal failure status. err: %v. Status: %+v", err, &failureStatus)
	}
	_, err = ctx.Write(bytes)
	if err != nil {
		log.Errorf("sendFailureResponse: failed to write failure status: %v", err)
	}
}

func handleTraceIngestionResponse(ctx *fasthttp.RequestCtx, numSpans int, numFailedSpans int) {
	if numFailedSpans == 0 {
		// This request was successful.
		response, err := proto.Marshal(&coltracepb.ExportTraceServiceResponse{})
		if err != nil {
			log.Errorf("handleTraceIngestionResponse: failed to marshal successful response. err: %v. NumSpans: %d", err, numSpans)
			ctx.SetStatusCode(fasthttp.StatusInternalServerError)
			return
		}
		_, err = ctx.Write(response)
		if err != nil {
			log.Errorf("handleTraceIngestionResponse: failed to write successful response. err: %v. NumSpans: %d", err, numSpans)
			ctx.SetStatusCode(fasthttp.StatusInternalServerError)
			return
		}

		ctx.SetStatusCode(fasthttp.StatusOK)
		return
	} else if numFailedSpans < numSpans {
		// This request was partially successful.
		traceResponse := coltracepb.ExportTraceServiceResponse{
			PartialSuccess: &coltracepb.ExportTracePartialSuccess{
				RejectedSpans: int64(numFailedSpans),
			},
		}

		response, err := proto.Marshal(&traceResponse)
		if err != nil {
			log.Errorf("handleTraceIngestionResponse: failed to marshal partially successful response: %v. NumSpans: %d, NumFailedSpans: %d, Trace Response: %v", err, numSpans, numFailedSpans, &traceResponse)
			ctx.SetStatusCode(fasthttp.StatusInternalServerError)
			return
		}
		_, err = ctx.Write(response)
		if err != nil {
			log.Errorf("handleTraceIngestionResponse: failed to write partially successful response: %v. NumSpans: %d, NumFailedSpans: %d, response: %v", err, numSpans, numFailedSpans, response)
			ctx.SetStatusCode(fasthttp.StatusInternalServerError)
			return
		}

		ctx.SetStatusCode(fasthttp.StatusOK)
		return
	} else {
		// Every span failed to be ingested.
		if numFailedSpans > numSpans {
			log.Errorf("handleTraceIngestionResponse: error in counting number of total and failed spans. Counted NumSpans: %d, Counted NumFailedSpans: %d", numSpans, numFailedSpans)
		}

		log.Errorf("handleTraceIngestionResponse: every span failed ingestion. NumSpans: %d, NumFailedSpans: %d", numSpans, numFailedSpans)
		setFailureResponse(ctx, fasthttp.StatusInternalServerError, "Every span failed ingestion")
		return
	}
}
