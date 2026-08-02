package handler

import (
	"context"
	"strconv"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type userIdempotentJSONOptions struct {
	NamespaceKeyByActor      bool
	EnforceKey               bool
	DetachedExecutionTimeout time.Duration
	FinalizationTimeout      time.Duration
	NoRetryOnExecutorError   bool
}

func executeUserIdempotentJSON(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	execute func(context.Context) (any, error),
) {
	executeUserIdempotentJSONWithOptions(c, scope, payload, ttl, userIdempotentJSONOptions{}, execute)
}

func executeUserIdempotentJSONWithOptions(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	opts userIdempotentJSONOptions,
	execute func(context.Context) (any, error),
) {
	if opts.EnforceKey {
		key, err := service.NormalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		if key == "" {
			response.ErrorFrom(c, service.ErrIdempotencyKeyRequired)
			return
		}
	}

	coordinator := service.DefaultIdempotencyCoordinator()
	if coordinator == nil {
		executionCtx := c.Request.Context()
		cancelExecution := func() {}
		if opts.DetachedExecutionTimeout > 0 {
			executionCtx, cancelExecution = context.WithTimeout(context.WithoutCancel(executionCtx), opts.DetachedExecutionTimeout)
		}
		defer cancelExecution()
		data, err := execute(executionCtx)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		response.Success(c, data)
		return
	}

	actorScope := "user:0"
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok {
		actorScope = "user:" + strconv.FormatInt(subject.UserID, 10)
	}
	keyNamespace := ""
	if opts.NamespaceKeyByActor {
		keyNamespace = actorScope
	}

	result, err := coordinator.Execute(c.Request.Context(), service.IdempotencyExecuteOptions{
		Scope:                    scope,
		ActorScope:               actorScope,
		KeyNamespace:             keyNamespace,
		Method:                   c.Request.Method,
		Route:                    c.FullPath(),
		IdempotencyKey:           c.GetHeader("Idempotency-Key"),
		Payload:                  payload,
		RequireKey:               true,
		TTL:                      ttl,
		DetachedExecutionTimeout: opts.DetachedExecutionTimeout,
		FinalizationTimeout:      opts.FinalizationTimeout,
		NoRetryOnExecutorError:   opts.NoRetryOnExecutorError,
	}, execute)
	if err != nil {
		if infraerrors.Code(err) == infraerrors.Code(service.ErrIdempotencyStoreUnavail) {
			service.RecordIdempotencyStoreUnavailable(c.FullPath(), scope, "handler_fail_close")
			logger.LegacyPrintf("handler.idempotency", "[Idempotency] store unavailable: method=%s route=%s scope=%s strategy=fail_close", c.Request.Method, c.FullPath(), scope)
		}
		if retryAfter := service.RetryAfterSecondsFromError(err); retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		response.ErrorFrom(c, err)
		return
	}
	if result != nil && result.Replayed {
		c.Header("X-Idempotency-Replayed", "true")
	}
	response.Success(c, result.Data)
}
