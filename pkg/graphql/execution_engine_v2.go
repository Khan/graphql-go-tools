package graphql

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"reflect"
	"strconv"
	"sync"
	"unsafe"

	lru "github.com/hashicorp/golang-lru"
	"github.com/jensneuse/abstractlogger"
	"github.com/jensneuse/graphql-go-tools/pkg/engine/datasource/introspection_datasource"
	"github.com/sirupsen/logrus"

	"github.com/jensneuse/graphql-go-tools/pkg/ast"
	"github.com/jensneuse/graphql-go-tools/pkg/astprinter"
	"github.com/jensneuse/graphql-go-tools/pkg/engine/datasource/httpclient"
	"github.com/jensneuse/graphql-go-tools/pkg/engine/plan"
	"github.com/jensneuse/graphql-go-tools/pkg/engine/resolve"
	"github.com/jensneuse/graphql-go-tools/pkg/operationreport"
	"github.com/jensneuse/graphql-go-tools/pkg/pool"
	"github.com/jensneuse/graphql-go-tools/pkg/postprocess"
)

type EngineResultWriter struct {
	buf           *bytes.Buffer
	flushCallback func(data []byte)
}

func NewEngineResultWriter() EngineResultWriter {
	return EngineResultWriter{
		buf: &bytes.Buffer{},
	}
}

func NewEngineResultWriterFromBuffer(buf *bytes.Buffer) EngineResultWriter {
	return EngineResultWriter{
		buf: buf,
	}
}

func (e *EngineResultWriter) SetFlushCallback(flushCb func(data []byte)) {
	e.flushCallback = flushCb
}

func (e *EngineResultWriter) Write(p []byte) (n int, err error) {
	return e.buf.Write(p)
}

func (e *EngineResultWriter) Read(p []byte) (n int, err error) {
	return e.buf.Read(p)
}

func (e *EngineResultWriter) Flush() {
	if e.flushCallback != nil {
		e.flushCallback(e.Bytes())
	}

	e.Reset()
}

func (e *EngineResultWriter) Len() int {
	return e.buf.Len()
}

func (e *EngineResultWriter) Bytes() []byte {
	return e.buf.Bytes()
}

func (e *EngineResultWriter) String() string {
	return e.buf.String()
}

func (e *EngineResultWriter) Reset() {
	e.buf.Reset()
}

func (e *EngineResultWriter) AsHTTPResponse(status int, headers http.Header) *http.Response {
	b := &bytes.Buffer{}

	switch headers.Get(httpclient.ContentEncodingHeader) {
	case "gzip":
		gzw := gzip.NewWriter(b)
		_, _ = gzw.Write(e.Bytes())
		_ = gzw.Close()
	case "deflate":
		fw, _ := flate.NewWriter(b, 1)
		_, _ = fw.Write(e.Bytes())
		_ = fw.Close()
	default:
		headers.Del(httpclient.ContentEncodingHeader) // delete unsupported compression header
		b = e.buf
	}

	res := &http.Response{}
	res.Body = ioutil.NopCloser(b)
	res.Header = headers
	res.StatusCode = status
	res.ContentLength = int64(b.Len())
	res.Header.Set("Content-Length", strconv.Itoa(b.Len()))
	return res
}

type internalExecutionContext struct {
	resolveContext *resolve.Context
	postProcessor  *postprocess.Processor
}

func newInternalExecutionContext() *internalExecutionContext {
	return &internalExecutionContext{
		resolveContext: resolve.NewContext(context.Background()),
		postProcessor:  postprocess.DefaultProcessor(),
	}
}

func (e *internalExecutionContext) prepare(ctx context.Context, variables []byte, request resolve.Request) {
	e.setContext(ctx)
	e.setVariables(variables)
	e.setRequest(request)
}

func (e *internalExecutionContext) setRequest(request resolve.Request) {
	e.resolveContext.Request = request
}

func (e *internalExecutionContext) setContext(ctx context.Context) {
	e.resolveContext.Context = ctx
}

func (e *internalExecutionContext) setVariables(variables []byte) {
	e.resolveContext.Variables = variables
}

func (e *internalExecutionContext) reset() {
	e.resolveContext.Free()
}

type ExecutionEngineV2 struct {
	logger                       abstractlogger.Logger
	config                       EngineV2Configuration
	planner                      *plan.Planner
	plannerMu                    sync.Mutex
	resolver                     *resolve.Resolver
	internalExecutionContextPool sync.Pool
	executionPlanCache           *lru.Cache
}

type WebsocketBeforeStartHook interface {
	OnBeforeStart(reqCtx context.Context, operation *Request) error
}

type ExecutionOptionsV2 func(ctx *internalExecutionContext)

func WithBeforeFetchHook(hook resolve.BeforeFetchHook) ExecutionOptionsV2 {
	return func(ctx *internalExecutionContext) {
		ctx.resolveContext.SetBeforeFetchHook(hook)
	}
}

func WithAfterFetchHook(hook resolve.AfterFetchHook) ExecutionOptionsV2 {
	return func(ctx *internalExecutionContext) {
		ctx.resolveContext.SetAfterFetchHook(hook)
	}
}

func WithAdditionalHttpHeaders(headers http.Header, excludeByKeys ...string) ExecutionOptionsV2 {
	return func(ctx *internalExecutionContext) {
		if len(headers) == 0 {
			return
		}

		if ctx.resolveContext.Request.Header == nil {
			ctx.resolveContext.Request.Header = make(http.Header)
		}

		excludeMap := make(map[string]bool)
		for _, key := range excludeByKeys {
			excludeMap[key] = true
		}

		for headerKey, headerValues := range headers {
			if excludeMap[headerKey] {
				continue
			}

			for _, headerValue := range headerValues {
				ctx.resolveContext.Request.Header.Add(headerKey, headerValue)
			}
		}
	}
}

func NewExecutionEngineV2(ctx context.Context, logger abstractlogger.Logger, engineConfig EngineV2Configuration) (*ExecutionEngineV2, error) {
	executionPlanCache, err := lru.New(1024)
	if err != nil {
		return nil, err
	}
	fetcher := resolve.NewFetcher(engineConfig.dataLoaderConfig.EnableSingleFlightLoader)

	introspectionCfg, err := introspection_datasource.NewIntrospectionConfigFactory(&engineConfig.schema.document)
	if err != nil {
		return nil, err
	}

	engineConfig.AddDataSource(introspectionCfg.BuildDataSourceConfiguration())
	for _, fieldCfg := range introspectionCfg.BuildFieldConfigurations() {
		engineConfig.AddFieldConfiguration(fieldCfg)
	}

	return &ExecutionEngineV2{
		logger:   logger,
		config:   engineConfig,
		planner:  plan.NewPlanner(ctx, engineConfig.plannerConfig),
		resolver: resolve.New(ctx, fetcher, engineConfig.dataLoaderConfig.EnableDataLoader),
		internalExecutionContextPool: sync.Pool{
			New: func() interface{} {
				return newInternalExecutionContext()
			},
		},
		executionPlanCache: executionPlanCache,
	}, nil
}

// NOTE: this only works for our setup, do not merge upstream!
func (e *ExecutionEngineV2) _debug(ctx context.Context, msg string) {
	if ctx == nil {
		return
	}
	ll, ok := e.logger.(*abstractlogger.LogrusLogger)
	if !ok {
		return // must be a test file
	}
	// This accesses the private field `ll.l`.  See
	// https://stackoverflow.com/questions/42664837/how-to-access-unexported-struct-fields
	logrusField := reflect.ValueOf(ll).Elem().FieldByName("l")
	logrusInterface := reflect.NewAt(
		logrusField.Type(),
		unsafe.Pointer(logrusField.UnsafeAddr()),
	).Elem().Interface()

	logrusInstance, ok := logrusInterface.(*logrus.Logger)
	if !ok {
		panic("Not a logrus logger?!")
	}
	logrusInstance.WithContext(ctx).Debug(msg)
}

func (e *ExecutionEngineV2) prepareExecutionContext(ctx context.Context, operation *Request, options ...ExecutionOptionsV2) (*internalExecutionContext, plan.Plan, error) {
	e._debug(ctx, "graphql.ExecutionEngineV2.prepareExecutionContext begin")
	if !operation.IsNormalized() {
		e._debug(ctx, "graphql.ExecutionEngineV2.prepareExecutionContext normalize")
		result, err := operation.Normalize(e.config.schema)
		if err != nil {
			return nil, nil, err
		}

		if !result.Successful {
			return nil, nil, result.Errors
		}
	}

	e._debug(ctx, "graphql.ExecutionEngineV2.prepareExecutionContext validate")
	result, err := operation.ValidateForSchema(e.config.schema)
	if err != nil {
		return nil, nil, err
	}
	if !result.Valid {
		return nil, nil, result.Errors
	}

	e._debug(ctx, "graphql.ExecutionEngineV2.prepareExecutionContext prepare")
	execContext := e.getExecutionCtx()
	execContext.prepare(ctx, operation.Variables, operation.request)

	for i := range options {
		e._debug(ctx, "graphql.ExecutionEngineV2.prepareExecutionContext apply option")
		options[i](execContext)
	}

	var report operationreport.Report
	cachedPlan := e.getCachedPlan(execContext, ctx, &operation.document, &e.config.schema.document, operation.OperationName, &report)
	if report.HasErrors() {
		return execContext, cachedPlan, report
	}

	return execContext, cachedPlan, nil
}

func (e *ExecutionEngineV2) ExecuteAndReturnPlan(ctx context.Context, operation *Request, writer resolve.FlushWriter, options ...ExecutionOptionsV2) (plan.Plan, error) {
	execContext, cachedPlan, err := e.prepareExecutionContext(ctx, operation, options...)
	if execContext != nil {
		defer e.putExecutionCtx(execContext)
	}
	if err != nil {
		return nil, err
	}

	e._debug(ctx, "graphql.ExecutionEngineV2.ExecuteAndReturnPlan executing plan")
	switch p := cachedPlan.(type) {
	case *plan.SynchronousResponsePlan:
		err = e.resolver.ResolveGraphQLResponse(execContext.resolveContext, p.Response, nil, writer)
	case *plan.SubscriptionResponsePlan:
		err = e.resolver.ResolveGraphQLSubscription(execContext.resolveContext, p.Response, writer)
	default:
		return nil, errors.New("execution of operation is not possible")
	}

	e._debug(ctx, "graphql.ExecutionEngineV2.ExecuteAndReturnPlan done")
	return cachedPlan, err
}

func (e *ExecutionEngineV2) Execute(ctx context.Context, operation *Request, writer resolve.FlushWriter, options ...ExecutionOptionsV2) error {
	_, err := e.ExecuteAndReturnPlan(ctx, operation, writer, options...)

	return err
}

func (e *ExecutionEngineV2) GetPlan(ctx context.Context, operation *Request, options ...ExecutionOptionsV2) (plan.Plan, error) {
	execContext, cachedPlan, err := e.prepareExecutionContext(ctx, operation, options...)
	if execContext != nil {
		defer e.putExecutionCtx(execContext)
	}
	if err != nil {
		return nil, err
	}
	return cachedPlan, nil
}

func (e *ExecutionEngineV2) getCachedPlan(ctx *internalExecutionContext, logCtx context.Context, operation, definition *ast.Document, operationName string, report *operationreport.Report) plan.Plan {
	e._debug(logCtx, "graphql.ExecutionEngineV2.getCachedPlan begin")
	hash := pool.Hash64.Get()
	hash.Reset()
	defer pool.Hash64.Put(hash)
	err := astprinter.Print(operation, definition, hash)
	if err != nil {
		report.AddInternalError(err)
		return nil
	}

	cacheKey := hash.Sum64()

	if cached, ok := e.executionPlanCache.Get(cacheKey); ok {
		if p, ok := cached.(plan.Plan); ok {
			e._debug(logCtx, fmt.Sprintf("graphql.ExecutionEngineV2.getCachedPlan cache-hit (opname %v, engine %p, key %v)", operationName, e, cacheKey))
			return p
		}
	}
	e._debug(logCtx, fmt.Sprintf("graphql.ExecutionEngineV2.getCachedPlan cache-miss (opname %v, engine %p, key %v)", operationName, e, cacheKey))

	e.plannerMu.Lock()
	defer e.plannerMu.Unlock()
	e._debug(logCtx, "graphql.ExecutionEngineV2.getCachedPlan planning")
	planResult := e.planner.Plan(operation, definition, operationName, report)
	if report.HasErrors() {
		return nil
	}

	e._debug(logCtx, "graphql.ExecutionEngineV2.getCachedPlan processing")
	p := ctx.postProcessor.Process(planResult)
	e.executionPlanCache.Add(cacheKey, p)
	e._debug(logCtx, "graphql.ExecutionEngineV2.getCachedPlan done")
	return p
}

func (e *ExecutionEngineV2) GetWebsocketBeforeStartHook() WebsocketBeforeStartHook {
	return e.config.websocketBeforeStartHook
}

func (e *ExecutionEngineV2) getExecutionCtx() *internalExecutionContext {
	return e.internalExecutionContextPool.Get().(*internalExecutionContext)
}

func (e *ExecutionEngineV2) putExecutionCtx(ctx *internalExecutionContext) {
	ctx.reset()
	e.internalExecutionContextPool.Put(ctx)
}
