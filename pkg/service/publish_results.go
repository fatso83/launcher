package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-kit/kit/endpoint"
	"github.com/go-kit/kit/log/level"
	"github.com/go-kit/kit/transport/http/jsonrpc"
	"github.com/kolide/kit/contexts/uuid"
	"github.com/osquery/osquery-go/plugin/distributed"

	pb "github.com/kolide/launcher/pkg/pb/launcher"
)

type resultCollection struct {
	NodeKey string `json:"node_key"`
	Results []distributed.Result
}

type publishResultsResponse struct {
	Message     string `json:"message"`
	NodeInvalid bool   `json:"node_invalid"`
	ErrorCode   string `json:"error_code,omitempty"`
	Err         error  `json:"err,omitempty"`
}

func decodeGRPCResultCollection(_ context.Context, grpcReq interface{}) (interface{}, error) {
	req := grpcReq.(*pb.ResultCollection)

	results := make([]distributed.Result, 0, len(req.Results))
	for _, result := range req.Results {
		// Iterate results
		rows := make([]map[string]string, 0, len(result.Rows))
		for _, row := range result.Rows {
			// Extract rows into map[string]string
			rowMap := make(map[string]string, len(row.Columns))
			for _, col := range row.Columns {
				rowMap[col.Name] = col.Value
			}
			rows = append(rows, rowMap)
		}
		results = append(results,
			distributed.Result{
				QueryName: result.Id,
				Status:    int(result.Status),
				Rows:      rows,
			},
		)
	}

	return resultCollection{
		Results: results,
		NodeKey: req.NodeKey,
	}, nil
}

func decodeJSONRPCResultCollection(_ context.Context, msg json.RawMessage) (interface{}, error) {
	var req resultCollection

	if err := json.Unmarshal(msg, &req); err != nil {
		return nil, &jsonrpc.Error{
			Code:    -32000,
			Message: fmt.Sprintf("couldn't unmarshal body to resultCollection: %s", err),
		}
	}
	return req, nil
}

func decodeJSONRPCPublishResultsResponse(_ context.Context, res jsonrpc.Response) (interface{}, error) {
	if res.Error != nil {
		return nil, *res.Error
	}
	var result publishResultsResponse
	err := json.Unmarshal(res.Result, &result)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling PublishResults response: %w", err)
	}

	return result, nil
}

func encodeGRPCResultCollection(_ context.Context, request interface{}) (interface{}, error) {
	req := request.(resultCollection)

	results := make([]*pb.ResultCollection_Result, 0, len(req.Results))
	for _, result := range req.Results {
		// Iterate results
		rows := make([]*pb.ResultCollection_Result_ResultRow, 0, len(result.Rows))
		for _, row := range result.Rows {
			// Extract rows into columns
			rowCols := make([]*pb.ResultCollection_Result_ResultRow_Column, 0, len(row))
			for col, val := range row {
				rowCols = append(rowCols, &pb.ResultCollection_Result_ResultRow_Column{
					Name:  col,
					Value: val,
				})
			}
			rows = append(rows, &pb.ResultCollection_Result_ResultRow{Columns: rowCols})
		}
		results = append(results,
			&pb.ResultCollection_Result{
				Id:     result.QueryName,
				Status: int32(result.Status),
				Rows:   rows,
			},
		)
	}

	return &pb.ResultCollection{
		NodeKey: req.NodeKey,
		Results: results,
	}, nil
}

func decodeGRPCPublishResultsResponse(_ context.Context, grpcReq interface{}) (interface{}, error) {
	req := grpcReq.(*pb.AgentApiResponse)
	return publishResultsResponse{
		Message:     req.Message,
		ErrorCode:   req.ErrorCode,
		NodeInvalid: req.NodeInvalid,
	}, nil
}

func encodeGRPCPublishResultsResponse(_ context.Context, request interface{}) (interface{}, error) {
	req := request.(publishResultsResponse)
	resp := &pb.AgentApiResponse{
		Message:     req.Message,
		ErrorCode:   req.ErrorCode,
		NodeInvalid: req.NodeInvalid,
	}
	return encodeResponse(resp, req.Err)
}

func encodeJSONRPCPublishResultsResponse(_ context.Context, obj interface{}) (json.RawMessage, error) {
	res, ok := obj.(publishResultsResponse)
	if !ok {
		return encodeJSONResponse(nil, fmt.Errorf("Asserting result to *publishResultsResponse failed. Got %T, %+v", obj, obj))
	}

	b, err := json.Marshal(res)
	return encodeJSONResponse(b, fmt.Errorf("marshal json response: %w", err))
}

func MakePublishResultsEndpoint(svc KolideService) endpoint.Endpoint {
	return func(ctx context.Context, request interface{}) (response interface{}, err error) {
		req := request.(resultCollection)
		message, errcode, valid, err := svc.PublishResults(ctx, req.NodeKey, req.Results)
		return publishResultsResponse{
			Message:     message,
			ErrorCode:   errcode,
			NodeInvalid: valid,
			Err:         err,
		}, nil
	}
}

// PublishResults implements KolideService.PublishResults
func (e Endpoints) PublishResults(ctx context.Context, nodeKey string, results []distributed.Result) (string, string, bool, error) {
	newCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	request := resultCollection{NodeKey: nodeKey, Results: results}
	response, err := e.PublishResultsEndpoint(newCtx, request)
	if err != nil {
		return "", "", false, err
	}
	resp := response.(publishResultsResponse)
	return resp.Message, resp.ErrorCode, resp.NodeInvalid, resp.Err
}

func (s *grpcServer) PublishResults(ctx context.Context, req *pb.ResultCollection) (*pb.AgentApiResponse, error) {
	_, rep, err := s.results.ServeGRPC(ctx, req)
	if err != nil {
		return nil, err
	}
	return rep.(*pb.AgentApiResponse), nil
}

func (mw logmw) PublishResults(ctx context.Context, nodeKey string, results []distributed.Result) (message, errcode string, reauth bool, err error) {
	defer func(begin time.Time) {
		resJSON, _ := json.Marshal(results)
		uuid, _ := uuid.FromContext(ctx)
		logger := level.Debug(mw.logger)
		if err != nil {
			logger = level.Info(mw.logger)
		}
		logger.Log(
			"method", "PublishResults",
			"uuid", uuid,
			"results", string(resJSON),
			"message", message,
			"errcode", errcode,
			"reauth", reauth,
			"err", err,
			"took", time.Since(begin),
		)
	}(time.Now())

	message, errcode, reauth, err = mw.next.PublishResults(ctx, nodeKey, results)
	return message, errcode, reauth, err
}

func (mw uuidmw) PublishResults(ctx context.Context, nodeKey string, results []distributed.Result) (message, errcode string, reauth bool, err error) {
	ctx = uuid.NewContext(ctx, uuid.NewForRequest())
	return mw.next.PublishResults(ctx, nodeKey, results)
}
