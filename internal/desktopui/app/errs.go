package app

import (
	"encoding/json"
	"errors"

	"github.com/digiogithub/agentd/internal/proto"
)

func asRemote(err error, re **proto.RemoteError) bool { return err != nil && errors.As(err, re) }

func asCoded(err error, ce **proto.CodedError) bool { return err != nil && errors.As(err, ce) }

func asAny(err error, target any) bool { return err != nil && errors.As(err, target) }

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
