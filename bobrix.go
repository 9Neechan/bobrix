package bobrix

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/tensved/bobrix/contracts"
	"github.com/tensved/bobrix/mxbot"
	"maunium.net/go/mautrix/event"
)

type ServiceHandler func(ctx mxbot.Ctx, r *contracts.MethodResponse, extra any)

// BobrixService - service for bot
// Binds service and adds handler for processing
type BobrixService struct {
	Service *contracts.Service
	Handler ServiceHandler

	IsOnline bool
}

// Bobrix - bot structure
// It is a connection structure between two components: it manages the bot and service contracts
type Bobrix struct {
	name          string
	bot           mxbot.Bot
	services      []*BobrixService
	Healthchecker Healthcheck

	logger *slog.Logger
}

type BobrixOpts func(*Bobrix)

func WithHealthcheck(healthCheckOpts ...HealthcheckOption) BobrixOpts {
	return func(bx *Bobrix) {

		healthcheck := NewHealthcheck(bx)

		for _, opt := range healthCheckOpts {
			opt(healthcheck)
		}

		bx.Healthchecker = healthcheck
	}
}

// NewBobrix - Bobrix constructor
func NewBobrix(mxBot mxbot.Bot, opts ...BobrixOpts) *Bobrix {
	bx := &Bobrix{
		name:     mxBot.Name(),
		bot:      mxBot,
		services: make([]*BobrixService, 0),
	}

	bx.logger = slog.Default().With("name", bx.name)

	for _, opt := range opts {
		opt(bx)
	}

	return bx
}

func (bx *Bobrix) Name() string {
	return bx.name
}

func (bx *Bobrix) Run(ctx context.Context) error {
	return bx.bot.StartListening(ctx)
}

func (bx *Bobrix) Stop(ctx context.Context) error {
	return bx.bot.StopListening(ctx)
}

// ConnectService - add service to the bot
// It is used for adding services
// It adds handler for processing the events of the service
func (bx *Bobrix) ConnectService(service *contracts.Service, handler func(ctx mxbot.Ctx, r *contracts.MethodResponse, extra any)) {
	bx.services = append(bx.services, &BobrixService{
		Service:  service,
		Handler:  handler,
		IsOnline: true,
	})
}

// Use - add handler to the bot
// It is used for adding event handlers (like middlewares or any other handler)
func (bx *Bobrix) Use(handler mxbot.EventHandler) {
	bx.bot.AddEventHandler(handler)
}

// GetService - return service by name. If the service is not found, it returns nil
// It is case-insensitive. All services are stored in lowercase
func (bx *Bobrix) GetService(name string) (*BobrixService, bool) {

	name = strings.ToLower(name)

	for _, botService := range bx.services {
		if strings.ToLower(botService.Service.Name) == name {
			return botService, true
		}
	}

	return nil, false
}

func (bx *Bobrix) Services() []*BobrixService {
	return bx.services
}

func (bx *Bobrix) Bot() mxbot.Bot {
	return bx.bot
}

type ServiceRequest struct {
	ServiceName string         `json:"service"`
	MethodName  string         `json:"method"`
	InputParams map[string]any `json:"inputs"`
}

type ServiceHandle func(evt *event.Event) *ServiceRequest

// ContractParserOpts - options for contract parser
// You can set hooks for pre-call and after-call
// For example, you can add logging or validation
type ContractParserOpts struct {
	PreCallHook   func(ctx mxbot.Ctx, req *ServiceRequest) (string, int, error)
	AfterCallHook func(ctx mxbot.Ctx, req *ServiceRequest, resp *contracts.MethodResponse) (string, int, error)
}

// SetContractParser - set contract parser. It is used for parsing events to service requests
// You can add hooks for pre-call and after-call with ContractParserOpts
func (bx *Bobrix) SetContractParser(parser func(evt *event.Event) *ServiceRequest, opts ...ContractParserOpts) {

	var opt ContractParserOpts

	if len(opts) > 0 {
		opt = opts[0]
	}

	bx.Use(
		mxbot.NewEventHandler(
			event.EventMessage,
			func(ctx mxbot.Ctx) error {

				request := parser(ctx.Event())

				// if request is nil, it means that the event does not match the contract
				// and the event should be ignored
				// or the service is not found
				if request == nil || request.ServiceName == "" {
					return nil
				}

				isHandled, unlocker := ctx.IsHandledWithUnlocker()
				if isHandled {
					return nil
				}

				defer unlocker()

				botService, ok := bx.GetService(strings.ToLower(request.ServiceName))
				if !ok {
					bx.logger.Error("service not found", "service", request.ServiceName, "services", fmt.Sprintf("%+v", bx.services[0].Service.Name))
					if err := ctx.ErrorAnswer(
						fmt.Sprintf(
							"Service \"%s\" not found",
							request.ServiceName,
						),
						contracts.ErrCodeServiceNotFound,
					); err != nil {
						return err
					}

					return nil
				}

				service := botService.Service
				handler := botService.Handler

				// if service is not online, send an error and return
				// IsOnline is true by default. But it can be changed with Healthcheck with WithAutoSwitch() option
				if !botService.IsOnline {
					handler(ctx, &contracts.MethodResponse{
						Err: fmt.Errorf("Service \"%s\" is offline", request.ServiceName),
					}, nil)
					return nil
				}

				opts := contracts.CallOpts{}

				if thread := ctx.Thread(); thread != nil {
					data := ConvertThreadToMessages(thread, ctx.Bot().FullName())
					opts.Messages = data
				}

				if opt.PreCallHook != nil {
					errMsg, errCode, err := opt.PreCallHook(ctx, request)
					if err != nil {
						if err := ctx.ErrorAnswer(errMsg, errCode); err != nil { //!
							return err
						}
						return err
					}
				}

				response, err := service.CallMethod(
					ctx.Context(),
					request.MethodName,
					request.InputParams,
					opts,
				)

				if err != nil {
					switch {
					case errors.Is(err, contracts.ErrMethodNotFound):
						if err := ctx.ErrorAnswer(fmt.Sprintf("Method \"%s\" not found", request.MethodName), contracts.ErrCodeMethodNotFound); err != nil {
							return err
						}

					default:
						if err := ctx.ErrorAnswer(err.Error(), response.ErrCode); err != nil {
							return err
						}
					}

					return nil
				}

				if opt.AfterCallHook != nil {
					errMsg, errCode, err := opt.AfterCallHook(ctx, request, response)
					if err != nil {
						if err := ctx.ErrorAnswer(errMsg, errCode); err != nil {
							return err
						}
						return err
					}
				}

				handler(ctx, response, nil)

				return nil

			},
		),
	)
}

func ConvertThreadToMessages(thread *mxbot.MessagesThread, botName string) contracts.Messages {
	msgs := make([]map[contracts.ChatRole]string, len(thread.Messages))

	for i, msg := range thread.Messages {
		msgs[i] = map[contracts.ChatRole]string{}

		body := msg.Content.AsMessage().Body

		if msg.Sender.String() == botName {
			msgs[i][contracts.AssistantRole] = body
		} else {
			msgs[i][contracts.UserRole] = body
		}
	}

	return msgs
}
