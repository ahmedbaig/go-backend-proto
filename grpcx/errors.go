package grpcx

import (
	"context"
	"errors"
	"net/http"

	"github.com/ahmedbaig/go-backend-http"
	commonv1 "github.com/ahmedbaig/go-backend-proto/gen/go/ahmedbaig/common/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func ToStatus(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(interface{ GRPCStatus() *status.Status }); ok {
		return err
	}
	var e *httpx.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		e = httpx.E(httpx.CodeGatewayTimeout).Wrap(err)
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request cancelled")
	default:
		e = httpx.AsError(err)
	}
	st := status.New(CodeForHTTP(e.Status), e.Message)
	info := &commonv1.ErrorInfo{
		Code:            string(e.Code),
		Message:         e.Message,
		CustomerActions: e.CustomerActions,
		SupportActions:  e.SupportActions,
		HttpStatus:      int32(e.Status),
	}
	for _, f := range e.Fields {
		info.Fields = append(info.Fields, &commonv1.FieldViolation{Field: f.Field, Rule: f.Rule, Message: f.Message})
	}
	if d, derr := st.WithDetails(info); derr == nil {
		st = d
	}
	return st.Err()
}

func FromStatus(err error) *httpx.Error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return httpx.E(httpx.CodeBadGateway).Wrap(err)
	}
	for _, d := range st.Details() {
		info, ok := d.(*commonv1.ErrorInfo)
		if !ok {
			continue
		}
		e := httpx.E(httpx.Code(info.Code)).WithMessage(info.Message)
		e.CustomerActions = info.CustomerActions
		e.SupportActions = info.SupportActions
		for _, f := range info.Fields {
			e.Fields = append(e.Fields, httpx.FieldError{Field: f.Field, Rule: f.Rule, Message: f.Message})
		}
		if info.HttpStatus > 0 {
			e.Status = int(info.HttpStatus)
		}
		return e.Wrap(err)
	}
	e := httpx.E(codeForGRPC(st.Code()))
	if st.Message() != "" && st.Code() != codes.Unavailable {
		e = e.WithMessage(st.Message())
	}
	return e.Wrap(err)
}

func CodeForHTTP(s int) codes.Code {
	switch s {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType:
		return codes.InvalidArgument
	case http.StatusUnauthorized:
		return codes.Unauthenticated
	case http.StatusForbidden:
		return codes.PermissionDenied
	case http.StatusNotFound, http.StatusGone:
		return codes.NotFound
	case http.StatusMethodNotAllowed:
		return codes.Unimplemented
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return codes.DeadlineExceeded
	case http.StatusConflict:
		return codes.AlreadyExists
	case http.StatusTooManyRequests:
		return codes.ResourceExhausted
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return codes.Unavailable
	}
	if s >= 400 && s < 500 {
		return codes.FailedPrecondition
	}
	return codes.Internal
}

func codeForGRPC(c codes.Code) httpx.Code {
	switch c {
	case codes.InvalidArgument, codes.OutOfRange:
		return httpx.CodeBadRequest
	case codes.Unauthenticated:
		return httpx.CodeUnauthenticated
	case codes.PermissionDenied:
		return httpx.CodeForbidden
	case codes.NotFound:
		return httpx.CodeNotFound
	case codes.AlreadyExists, codes.Aborted, codes.FailedPrecondition:
		return httpx.CodeConflict
	case codes.ResourceExhausted:
		return httpx.CodeRateLimited
	case codes.DeadlineExceeded:
		return httpx.CodeGatewayTimeout
	case codes.Unavailable:
		return httpx.CodeUnavailable
	case codes.Unimplemented:
		return httpx.CodeMethod
	}
	return httpx.CodeBadGateway
}
