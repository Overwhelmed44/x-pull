// Package xray adapts Xray's official protobuf API to the reconciliation model.
package xray

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/x-pull/x-pull/internal/user"
	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/proxy/trojan"
	"github.com/xtls/xray-core/proxy/vless"
	"github.com/xtls/xray-core/proxy/vmess"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type Client struct {
	conn    *grpc.ClientConn
	api     command.HandlerServiceClient
	timeout time.Duration
}

// Xray's API uses plaintext gRPC. Keep it on loopback or a trusted private link.
func New(address string, timeout time.Duration, maxReceiveBytes int) (*Client, error) {
	conn, err := grpc.NewClient("passthrough:///"+address, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxReceiveBytes)))
	if err != nil {
		return nil, fmt.Errorf("initialize Xray connection: %w", err)
	}
	return &Client{conn: conn, api: command.NewHandlerServiceClient(conn), timeout: timeout}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) List(ctx context.Context, target user.Target) ([]user.User, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, err := c.api.GetInboundUsers(ctx, &command.GetInboundUserRequest{Tag: target.Tag})
	if err != nil {
		return nil, rpcError("GetInboundUsers", err)
	}
	users := make([]user.User, 0, len(resp.Users))
	for _, p := range resp.Users {
		if p == nil {
			return nil, fmt.Errorf("Xray returned a nil user")
		}
		u, err := decode(p, target.Protocol, strings.HasPrefix(strings.ToLower(p.Email), target.EmailPrefix))
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, nil
}

func (c *Client) Add(ctx context.Context, target user.Target, u user.User) error {
	account, err := encode(u, target.Protocol)
	if err != nil {
		return err
	}
	return c.alter(ctx, target.Tag, &command.AddUserOperation{User: &protocol.User{
		Email: u.Email, Level: u.Level, Account: serial.ToTypedMessage(account)}})
}

func (c *Client) Remove(ctx context.Context, target user.Target, email string) error {
	return c.alter(ctx, target.Tag, &command.RemoveUserOperation{Email: email})
}

func (c *Client) alter(ctx context.Context, tag string, op proto.Message) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	_, err := c.api.AlterInbound(ctx, &command.AlterInboundRequest{Tag: tag, Operation: serial.ToTypedMessage(op)})
	if err != nil {
		return rpcError("AlterInbound", err)
	}
	return nil
}

// Server error messages can include UUIDs/passwords; only log the status code.
func rpcError(method string, err error) error {
	return fmt.Errorf("Xray %s failed (%s)", method, status.Code(err))
}

func encode(u user.User, kind string) (proto.Message, error) {
	switch kind {
	case "vless":
		return &vless.Account{Id: u.ID, Flow: u.Flow}, nil
	case "vmess":
		return &vmess.Account{Id: u.ID}, nil
	case "trojan":
		return &trojan.Account{Password: u.Password}, nil
	default:
		return nil, fmt.Errorf("unsupported protocol")
	}
}

func decode(p *protocol.User, kind string, managed bool) (user.User, error) {
	u := user.User{Email: strings.ToLower(p.Email), Level: p.Level}
	if p.Account == nil {
		return u, fmt.Errorf("live user has no account")
	}
	var account proto.Message
	switch kind {
	case "vless":
		account = &vless.Account{}
	case "vmess":
		account = &vmess.Account{}
	case "trojan":
		account = &trojan.Account{}
	default:
		return u, fmt.Errorf("unsupported protocol")
	}
	if p.Account.Type != string(account.ProtoReflect().Descriptor().FullName()) {
		return u, fmt.Errorf("live account type does not match configured protocol")
	}
	if err := proto.Unmarshal(p.Account.Value, account); err != nil {
		return u, fmt.Errorf("cannot decode live account")
	}
	switch a := account.(type) {
	case *vless.Account:
		// Refuse to erase advanced account settings outside this service's schema.
		known := &vless.Account{Id: a.Id, Flow: a.Flow}
		if managed && !proto.Equal(a, known) {
			return u, fmt.Errorf("managed VLESS user has unsupported account settings")
		}
		u.ID, u.Flow = a.Id, a.Flow
	case *vmess.Account:
		if managed && a.TestsEnabled != "" {
			return u, fmt.Errorf("managed VMess user has unsupported account settings")
		}
		u.ID = a.Id
	case *trojan.Account:
		u.Password = a.Password
	}
	return u, nil
}
