package eventsx

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ahmedbaig/go-backend-http"
	eventsv1 "github.com/ahmedbaig/go-backend-proto/gen/go/ahmedbaig/events/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func New(ctx context.Context, topic, typ string, data any) (*eventsv1.Event, error) {
	s, err := toStruct(data)
	if err != nil {
		return nil, err
	}
	return &eventsv1.Event{
		Topic:         topic,
		Type:          typ,
		Data:          s,
		CorrelationId: httpx.CorrelationID(ctx),
		RequestId:     httpx.RequestID(ctx),
		At:            timestamppb.New(time.Now().UTC()),
	}, nil
}

func toStruct(data any) (*structpb.Struct, error) {
	if data == nil {
		return nil, nil
	}
	b, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return structpb.NewStruct(m)
}

func Marshal(ev *eventsv1.Event) ([]byte, error) {
	return protojson.MarshalOptions{}.Marshal(ev)
}

func Unmarshal(b []byte) (*eventsv1.Event, error) {
	ev := &eventsv1.Event{}
	return ev, protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(b, ev)
}

func Decode[T any](ev *eventsv1.Event) (T, error) {
	var v T
	if ev.GetData() == nil {
		return v, nil
	}
	b, err := ev.GetData().MarshalJSON()
	if err != nil {
		return v, err
	}
	return v, json.Unmarshal(b, &v)
}
