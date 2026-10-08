package poller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/proxy/trojan"
	"github.com/xtls/xray-core/proxy/vless"
	"github.com/xtls/xray-core/proxy/vmess"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

type xrayClient struct {
	conn    *grpc.ClientConn
	api     command.HandlerServiceClient
	timeout time.Duration
}

func newXrayClient(address string, timeout time.Duration) (*xrayClient, error) {
	conn, err := grpc.NewClient("passthrough:///"+address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &xrayClient{conn: conn, api: command.NewHandlerServiceClient(conn), timeout: timeout}, nil
}

func (c *xrayClient) list(ctx context.Context, inbound Inbound) (map[string]User, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, err := c.api.GetInboundUsers(ctx, &command.GetInboundUserRequest{Tag: inbound.Tag})
	if err != nil {
		return nil, err
	}
	users := make(map[string]User, len(resp.Users))
	for _, p := range resp.Users {
		email := strings.ToLower(p.GetEmail())
		if !strings.HasPrefix(email, inbound.EmailPrefix) {
			continue
		}
		u := User{Email: email, Level: p.GetLevel()}
		var account proto.Message
		switch inbound.Protocol {
		case "vless":
			account = &vless.Account{}
		case "vmess":
			account = &vmess.Account{}
		case "trojan":
			account = &trojan.Account{}
		default:
			return nil, fmt.Errorf("unsupported protocol %q", inbound.Protocol)
		}
		if err := proto.Unmarshal(p.GetAccount().GetValue(), account); err != nil {
			return nil, err
		}
		switch a := account.(type) {
		case *vless.Account:
			u.ID, u.Flow = a.Id, a.Flow
		case *vmess.Account:
			u.ID = a.Id
		case *trojan.Account:
			u.Password = a.Password
		}
		users[email] = u
	}
	return users, nil
}

func (c *xrayClient) add(ctx context.Context, inbound Inbound, u User) error {
	var account proto.Message
	switch inbound.Protocol {
	case "vless":
		account = &vless.Account{Id: u.ID, Flow: u.Flow}
	case "vmess":
		account = &vmess.Account{Id: u.ID}
	case "trojan":
		account = &trojan.Account{Password: u.Password}
	default:
		return fmt.Errorf("unsupported protocol %q", inbound.Protocol)
	}
	return c.alter(ctx, inbound.Tag, &command.AddUserOperation{User: &protocol.User{
		Email: u.Email, Level: u.Level, Account: serial.ToTypedMessage(account)}})
}

func (c *xrayClient) remove(ctx context.Context, tag, email string) error {
	return c.alter(ctx, tag, &command.RemoveUserOperation{Email: email})
}

func (c *xrayClient) alter(ctx context.Context, tag string, op proto.Message) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	_, err := c.api.AlterInbound(ctx, &command.AlterInboundRequest{Tag: tag, Operation: serial.ToTypedMessage(op)})
	return err
}
