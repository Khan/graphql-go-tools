//go:generate mockgen -self_package=github.com/jensneuse/go-data-resolver/pkg/resolve -destination=resolve_mock_test.go -package=resolve . DataSource

package resolve

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash"
	"io"
	"strconv"
	"sync"

	"github.com/buger/jsonparser"
	"github.com/cespare/xxhash"
)

var (
	lBrace           = []byte("{")
	rBrace           = []byte("}")
	lBrack           = []byte("[")
	rBrack           = []byte("]")
	comma            = []byte(",")
	colon            = []byte(":")
	quote            = []byte("\"")
	null             = []byte("null")
	literalData      = []byte("data")
	literalErrors    = []byte("Errors")
	literalMessage   = []byte("message")
	literalLocations = []byte("locations")
	literalPath      = []byte("path")
)

var errNonNullableFieldValueIsNull = errors.New("non Nullable field value is null")
var errTypeNameSkipped = errors.New("skipped because of __typename condition")

type Node interface {
	NodeKind() NodeKind
}

type NodeKind int
type FetchKind int

const (
	NodeKindObject NodeKind = iota + 1
	NodeKindEmptyObject
	NodeKindArray
	NodeKindEmptyArray
	NodeKindNull
	NodeKindString
	NodeKindBoolean
	NodeKindInteger
	NodeKindFloat

	FetchKindSingle FetchKind = iota + 1
	FetchKindParallel
)

type Context struct {
	context.Context
	Variables []byte
}

type Fetch interface {
	FetchKind() FetchKind
}

type Fetches []Fetch

func (f *Fetches) AppendIfUnique(fetch Fetch) {
	for i := range *f {
		if fetch == (*f)[i] {
			return
		}
	}
	*f = append(*f, fetch)
}

type DataSource interface {
	Load(ctx context.Context, input []byte, bufPair *BufPair) (err error)
	UniqueIdentifier() []byte
}

type Resolver struct {
	EnableSingleFlightLoader bool
	resultSetPool            sync.Pool
	byteSlicesPool           sync.Pool
	waitGroupPool            sync.Pool
	bufPairPool              sync.Pool
	bufPairSlicePool         sync.Pool
	errChanPool              sync.Pool
	hash64Pool               sync.Pool
	inflightFetchPool        sync.Pool
	inflightFetchMu          sync.Mutex
	inflightFetches          map[uint64]*inflightFetch
}

type inflightFetch struct {
	waitLoad sync.WaitGroup
	waitFree sync.WaitGroup
	err      error
	bufPair  BufPair
}

func New() *Resolver {
	return &Resolver{
		resultSetPool: sync.Pool{
			New: func() interface{} {
				return &resultSet{
					buffers: make(map[int]*BufPair, 8),
				}
			},
		},
		byteSlicesPool: sync.Pool{
			New: func() interface{} {
				slice := make([][]byte, 0, 24)
				return &slice
			},
		},
		waitGroupPool: sync.Pool{
			New: func() interface{} {
				return &sync.WaitGroup{}
			},
		},
		bufPairPool: sync.Pool{
			New: func() interface{} {
				pair := BufPair{
					Data:   bytes.NewBuffer(make([]byte, 0, 1024)),
					Errors: bytes.NewBuffer(make([]byte, 0, 1024)),
				}
				return &pair
			},
		},
		bufPairSlicePool: sync.Pool{
			New: func() interface{} {
				slice := make([]*BufPair, 0, 24)
				return &slice
			},
		},
		errChanPool: sync.Pool{
			New: func() interface{} {
				return make(chan error, 1)
			},
		},
		hash64Pool: sync.Pool{
			New: func() interface{} {
				return xxhash.New()
			},
		},
		inflightFetchPool: sync.Pool{
			New: func() interface{} {
				return &inflightFetch{
					bufPair: BufPair{
						Data:   bytes.NewBuffer(make([]byte, 0, 1024)),
						Errors: bytes.NewBuffer(make([]byte, 0, 1024)),
					},
				}
			},
		},
		inflightFetches: map[uint64]*inflightFetch{},
	}
}

func (r *Resolver) writeSafe(err error, writer io.Writer, data []byte) error {
	if err != nil {
		return err
	}
	_, err = writer.Write(data)
	return err
}

// nolint
func (r *Resolver) writeErrSafe(err error, writer io.Writer, message, locations, path []byte) error {
	if err != nil {
		return err
	}
	_, err = writer.Write(lBrace)
	err = r.resolveObjectFieldSafe(err, writer, literalMessage, message)
	if err != nil {
		return err
	}
	if locations != nil {
		_, err = writer.Write(comma)
		if err != nil {
			return err
		}
		err = r.resolveObjectFieldSafe(err, writer, literalLocations, locations)
		if err != nil {
			return err
		}
	}
	if locations != nil {
		_, err = writer.Write(comma)
		if err != nil {
			return err
		}
		err = r.resolveObjectFieldSafe(err, writer, literalPath, locations)
		if err != nil {
			return err
		}
	}
	_, err = writer.Write(rBrace)
	return err
}

// nolint
func (r *Resolver) resolveObjectFieldSafe(err error, writer io.Writer, fieldName, fieldContent []byte) error {
	if err != nil {
		return err
	}
	if _, err = writer.Write(quote); err != nil {
		return err
	}
	if _, err = writer.Write(fieldName); err != nil {
		return err
	}
	if _, err = writer.Write(quote); err != nil {
		return err
	}
	if _, err = writer.Write(colon); err != nil {
		return err
	}
	_, err = writer.Write(fieldContent)
	return err
}

func (r *Resolver) resolveNode(ctx Context, node Node, data []byte, bufPair *BufPair) (err error) {
	switch n := node.(type) {
	case *Object:
		return r.resolveObject(ctx, n, data, bufPair)
	case *Array:
		return r.resolveArray(ctx, n, data, bufPair)
	case *Null:
		return r.resolveNull(bufPair.Data)
	case *String:
		return r.resolveString(n, data, bufPair)
	case *Boolean:
		return r.resolveBoolean(n, data, bufPair)
	case *Integer:
		return r.resolveInteger(n, data, bufPair)
	case *Float:
		return r.resolveFloat(n, data, bufPair)
	case *EmptyObject:
		return r.resolveEmptyObject(bufPair.Data)
	case *EmptyArray:
		return r.resolveEmptyArray(bufPair.Data)
	default:
		return
	}
}

func (r *Resolver) ResolveGraphQLResponse(ctx Context, response *GraphQLResponse, data []byte, writer io.Writer) (err error) {

	buf := r.getBufPair()
	defer r.freeBufPair(buf)

	err = r.resolveNode(ctx, response.Data, data, buf)
	if err != nil {
		return
	}

	hasErrors := buf.Errors.Len() != 0
	hasData := buf.Data.Len() != 0

	err = r.writeSafe(err, writer, lBrace)

	if hasErrors {
		err = r.writeSafe(err, writer, quote)
		err = r.writeSafe(err, writer, literalErrors)
		err = r.writeSafe(err, writer, quote)
		err = r.writeSafe(err, writer, colon)
		err = r.writeSafe(err, writer, lBrack)
		_, err = buf.Errors.WriteTo(writer)
		err = r.writeSafe(err, writer, rBrack)
	}

	if hasData {
		if hasErrors {
			err = r.writeSafe(err, writer, comma)
		}
		err = r.writeSafe(err, writer, quote)
		err = r.writeSafe(err, writer, literalData)
		err = r.writeSafe(err, writer, quote)
		err = r.writeSafe(err, writer, colon)
		_, err = buf.Data.WriteTo(writer)
	}

	err = r.writeSafe(err, writer, rBrace)

	return
}

func (r *Resolver) resolveEmptyArray(writer io.Writer) (err error) {
	err = r.writeSafe(nil, writer, lBrack)
	return r.writeSafe(err, writer, rBrack)
}

func (r *Resolver) resolveEmptyObject(writer io.Writer) (err error) {
	err = r.writeSafe(err, writer, lBrace)
	return r.writeSafe(err, writer, rBrace)
}

func (r *Resolver) resolveArray(ctx Context, array *Array, data []byte, arrayBuf *BufPair) (err error) {

	arrayItems := r.byteSlicesPool.Get().(*[][]byte)
	defer func() {
		*arrayItems = (*arrayItems)[:0]
		r.byteSlicesPool.Put(arrayItems)
	}()

	_, err = jsonparser.ArrayEach(data, func(value []byte, dataType jsonparser.ValueType, offset int, err error) {
		*arrayItems = append(*arrayItems, value)
	}, array.Path...)

	if len(*arrayItems) == 0 {
		if !array.Nullable {
			return errNonNullableFieldValueIsNull
		}
		return r.resolveNull(arrayBuf.Data)
	}

	if array.ResolveAsynchronous {
		return r.resolveArrayAsynchronous(ctx, array, arrayItems, arrayBuf)
	}
	return r.resolveArraySynchronous(ctx, array, arrayItems, arrayBuf)
}

func (r *Resolver) resolveArraySynchronous(ctx Context, array *Array, arrayItems *[][]byte, arrayBuf *BufPair) (err error) {

	itemBuf := r.getBufPair()
	defer r.freeBufPair(itemBuf)

	err = r.writeSafe(err, arrayBuf.Data, lBrack)
	var (
		hasPreviousItem bool
		dataWritten     int
	)
	for i := range *arrayItems {
		err = r.resolveNode(ctx, array.Item, (*arrayItems)[i], itemBuf)
		if err != nil {
			if errors.Is(err, errNonNullableFieldValueIsNull) && array.Nullable {
				arrayBuf.Data.Reset()
				return r.resolveNull(arrayBuf.Data)
			}
			if errors.Is(err, errTypeNameSkipped) {
				err = nil
				continue
			}
			return
		}
		dataWritten, _, err = r.MergeBufPairs(itemBuf, arrayBuf, hasPreviousItem)
		if !hasPreviousItem && dataWritten != 0 {
			hasPreviousItem = true
		}
	}

	return r.writeSafe(err, arrayBuf.Data, rBrack)
}

func (r *Resolver) resolveArrayAsynchronous(ctx Context, array *Array, arrayItems *[][]byte, arrayBuf *BufPair) (err error) {

	err = r.writeSafe(err, arrayBuf.Data, lBrack)

	bufSlice := r.getBufPairSlice()
	defer r.freeBufPairSlice(bufSlice)

	wg := r.getWaitGroup()
	defer r.freeWaitGroup(wg)

	errCh := r.getErrChan()
	defer r.freeErrChan(errCh)

	wg.Add(len(*arrayItems))

	for i := range *arrayItems {
		itemBuf := r.getBufPair()
		*bufSlice = append(*bufSlice, itemBuf)
		itemData := (*arrayItems)[i]
		go func() {
			if e := r.resolveNode(ctx, array.Item, itemData, itemBuf); e != nil && !errors.Is(e, errTypeNameSkipped) {
				select {
				case errCh <- e:
				default:
				}
			}
			wg.Done()
		}()
	}

	wg.Wait()

	select {
	case err = <-errCh:
	default:
	}

	if err != nil {
		if errors.Is(err, errNonNullableFieldValueIsNull) && array.Nullable {
			arrayBuf.Data.Reset()
			return r.resolveNull(arrayBuf.Data)
		}
		return
	}

	var (
		hasPreviousItem bool
		dataWritten     int
	)
	for i := range *bufSlice {
		dataWritten, _, err = r.MergeBufPairs((*bufSlice)[i], arrayBuf, hasPreviousItem)
		if !hasPreviousItem && dataWritten != 0 {
			hasPreviousItem = true
		}
	}

	return r.writeSafe(err, arrayBuf.Data, rBrack)
}

func (r *Resolver) resolveInteger(integer *Integer, data []byte, integerBuf *BufPair) (err error) {
	value, dataType, _, err := jsonparser.Get(data, integer.Path...)
	if err != nil || dataType != jsonparser.Number {
		if !integer.Nullable {
			return errNonNullableFieldValueIsNull
		}
		return r.resolveNull(integerBuf.Data)
	}
	return r.writeSafe(nil, integerBuf.Data, value)
}

func (r *Resolver) resolveFloat(floatValue *Float, data []byte, integerBuf *BufPair) (err error) {
	value, dataType, _, err := jsonparser.Get(data, floatValue.Path...)
	if err != nil || dataType != jsonparser.Number {
		if !floatValue.Nullable {
			return errNonNullableFieldValueIsNull
		}
		return r.resolveNull(integerBuf.Data)
	}
	return r.writeSafe(nil, integerBuf.Data, value)
}

func (r *Resolver) resolveBoolean(boolean *Boolean, data []byte, booleanBuf *BufPair) (err error) {
	value, valueType, _, err := jsonparser.Get(data, boolean.Path...)
	if err != nil || valueType != jsonparser.Boolean {
		if !boolean.Nullable {
			return errNonNullableFieldValueIsNull
		}
		return r.resolveNull(booleanBuf.Data)
	}
	return r.writeSafe(nil, booleanBuf.Data, value)
}

func (r *Resolver) resolveString(str *String, data []byte, stringBuf *BufPair) (err error) {
	value, valueType, _, err := jsonparser.Get(data, str.Path...)
	if err != nil || valueType != jsonparser.String {
		if !str.Nullable {
			return errNonNullableFieldValueIsNull
		}
		return r.resolveNull(stringBuf.Data)
	}
	if data == nil && !str.Nullable {
		return errNonNullableFieldValueIsNull
	}
	err = r.writeSafe(err, stringBuf.Data, quote)
	err = r.writeSafe(err, stringBuf.Data, value)
	return r.writeSafe(err, stringBuf.Data, quote)
}

func (r *Resolver) resolveNull(writer io.Writer) (err error) {
	return r.writeSafe(nil, writer, null)
}

func (r *Resolver) resolveObject(ctx Context, object *Object, data []byte, objectBuf *BufPair) (err error) {

	if len(object.Path) != 0 {
		data, _, _, _ = jsonparser.Get(data, object.Path...)
	}

	var set *resultSet
	if object.Fetch != nil {
		set = r.resultSetPool.Get().(*resultSet)
		defer r.freeResultSet(set)
		err = r.resolveFetch(ctx, object.Fetch, data, set)
		if err != nil {
			return
		}
		for i := range set.buffers {
			_, err = r.MergeBufPairErrors(set.buffers[i], objectBuf)
		}
	}

	fieldBuf := r.getBufPair()
	defer r.freeBufPair(fieldBuf)

	typeNameSkip := false
	first := true
	for i := range object.FieldSets {
		var fieldSetData []byte
		if set != nil && object.FieldSets[i].HasBuffer {
			buffer, ok := set.buffers[object.FieldSets[i].BufferID]
			if ok {
				fieldSetData = buffer.Data.Bytes()
			}
		} else {
			fieldSetData = data
		}

		if object.FieldSets[i].OnTypeName != nil {
			typeName, _, _, _ := jsonparser.Get(fieldSetData, "__typename")
			if !bytes.Equal(typeName, object.FieldSets[i].OnTypeName) {
				typeNameSkip = true
				continue
			}
		}

		for j := range object.FieldSets[i].Fields {
			if first {
				err = r.writeSafe(err, objectBuf.Data, lBrace)
				first = false
			} else {
				err = r.writeSafe(err, objectBuf.Data, comma)
			}
			err = r.writeSafe(err, objectBuf.Data, quote)
			err = r.writeSafe(err, objectBuf.Data, object.FieldSets[i].Fields[j].Name)
			err = r.writeSafe(err, objectBuf.Data, quote)
			err = r.writeSafe(err, objectBuf.Data, colon)
			if err != nil {
				return
			}
			err = r.resolveNode(ctx, object.FieldSets[i].Fields[j].Value, fieldSetData, fieldBuf)
			if err != nil {
				if errors.Is(err, errNonNullableFieldValueIsNull) && object.Nullable {
					objectBuf.Data.Reset()
					return r.writeSafe(nil, objectBuf.Data, null)
				}
				return
			}
			_, _, err = r.MergeBufPairs(fieldBuf, objectBuf, false)
		}
	}
	if first {
		if !object.Nullable {
			if typeNameSkip {
				return errTypeNameSkipped
			}
			return errNonNullableFieldValueIsNull
		}
		return r.resolveNull(objectBuf.Data)
	}
	return r.writeSafe(err, objectBuf.Data, rBrace)
}

func (r *Resolver) freeResultSet(set *resultSet) {
	for i := range set.buffers {
		set.buffers[i].Reset()
		r.bufPairPool.Put(set.buffers[i])
		delete(set.buffers, i)
	}
	r.resultSetPool.Put(set)
}

func (r *Resolver) resolveFetch(ctx Context, fetch Fetch, data []byte, set *resultSet) (err error) {
	switch f := fetch.(type) {
	case *SingleFetch:
		preparedInput := r.getBufPair()
		defer r.freeBufPair(preparedInput)
		err = r.prepareSingleFetch(ctx, f, data, set, preparedInput.Data)
		if err != nil {
			return err
		}
		err = r.resolveSingleFetch(ctx, f, preparedInput.Data, set.buffers[f.BufferId])
	case *ParallelFetch:
		preparedInputs := r.getBufPairSlice()
		defer r.freeBufPairSlice(preparedInputs)
		for i := range f.Fetches {
			preparedInput := r.getBufPair()
			err = r.prepareSingleFetch(ctx, f.Fetches[i], data, set, preparedInput.Data)
			if err != nil {
				return err
			}
			*preparedInputs = append(*preparedInputs, preparedInput)
		}
		wg := r.getWaitGroup()
		defer r.freeWaitGroup(wg)
		for i := range f.Fetches {
			preparedInput := (*preparedInputs)[i]
			singleFetch := f.Fetches[i]
			buf := set.buffers[f.Fetches[i].BufferId]
			wg.Add(1)
			go func(s *SingleFetch, buf *BufPair) {
				_ = r.resolveSingleFetch(ctx, s, preparedInput.Data, buf)
				wg.Done()
			}(singleFetch, buf)
		}
		wg.Wait()
	}
	return
}

func (r *Resolver) prepareSingleFetch(ctx Context, fetch *SingleFetch, data []byte, set *resultSet, preparedInput *bytes.Buffer) (err error) {
	err = fetch.InputTemplate.Render(ctx, data, preparedInput)
	buf := r.getBufPair()
	set.buffers[fetch.BufferId] = buf
	return
}

func (r *Resolver) resolveSingleFetch(ctx Context, fetch *SingleFetch, preparedInput *bytes.Buffer, buf *BufPair) (err error) {

	if !r.EnableSingleFlightLoader || fetch.DisallowSingleFlight {
		return fetch.DataSource.Load(ctx.Context, preparedInput.Bytes(), buf)
	}

	h := r.hash64Pool.Get().(hash.Hash64)
	_, _ = h.Write(fetch.DataSource.UniqueIdentifier())
	_, _ = h.Write(preparedInput.Bytes())
	fetchID := h.Sum64()
	h.Reset()
	r.hash64Pool.Put(h)

	r.inflightFetchMu.Lock()
	inflight, ok := r.inflightFetches[fetchID]
	if ok {
		inflight.waitFree.Add(1)
		defer inflight.waitFree.Done()
		r.inflightFetchMu.Unlock()
		inflight.waitLoad.Wait()
		if inflight.bufPair.HasData() {
			_, err = buf.Data.Write(inflight.bufPair.Data.Bytes())
			if err != nil {
				return
			}
		}
		if inflight.bufPair.HasErrors() {
			_, err = buf.Errors.Write(inflight.bufPair.Errors.Bytes())
			if err != nil {
				return
			}
		}
		return inflight.err
	}

	inflight = r.getInflightFetch()
	inflight.waitLoad.Add(1)
	r.inflightFetches[fetchID] = inflight

	r.inflightFetchMu.Unlock()

	err = fetch.DataSource.Load(ctx.Context, preparedInput.Bytes(), &inflight.bufPair)
	inflight.err = err

	if inflight.bufPair.HasData() {
		_, err = buf.Data.Write(inflight.bufPair.Data.Bytes())
	}

	if inflight.bufPair.HasErrors() {
		_, err = buf.Errors.Write(inflight.bufPair.Errors.Bytes())
	}

	inflight.waitLoad.Done()

	r.inflightFetchMu.Lock()
	delete(r.inflightFetches, fetchID)
	r.inflightFetchMu.Unlock()

	go func() {
		inflight.waitFree.Wait()
		r.freeInflightFetch(inflight)
	}()

	return
}

type Object struct {
	Nullable  bool
	Path      []string
	FieldSets []FieldSet
	Fetch     Fetch
}

func (_ *Object) NodeKind() NodeKind {
	return NodeKindObject
}

type EmptyObject struct{}

func (_ *EmptyObject) NodeKind() NodeKind {
	return NodeKindEmptyObject
}

type EmptyArray struct{}

func (_ *EmptyArray) NodeKind() NodeKind {
	return NodeKindEmptyArray
}

type FieldSet struct {
	OnTypeName []byte
	BufferID   int
	HasBuffer  bool
	Fields     []Field
}

type Field struct {
	Name  []byte
	Value Node
}

type Null struct {
}

func (_ *Null) NodeKind() NodeKind {
	return NodeKindNull
}

type resultSet struct {
	buffers map[int]*BufPair
}

type SingleFetch struct {
	BufferId   int
	Input      string
	DataSource DataSource
	Variables  Variables
	// DisallowSingleFlight is used for write operations like mutations, POST, DELETE etc. to disable singleFlight
	// By default SingleFlight for fetches is disabled and needs to be enabled on the Resolver first
	// If the resolver allows SingleFlight it's up the each individual DataSource Planner to decide whether an Operation
	// should be allowed to use SingleFlight
	DisallowSingleFlight bool
	InputTemplate        InputTemplate
}

type InputTemplate struct {
	Segments []TemplateSegment
}

func (i *InputTemplate) Render(ctx Context, data []byte, preparedInput *bytes.Buffer) (err error) {
	var (
		variableSource []byte
	)
	for j := range i.Segments {
		switch i.Segments[j].SegmentType {
		case StaticSegmentType:
			_, err = preparedInput.Write(i.Segments[j].Data)
		case VariableSegmentType:
			switch i.Segments[j].VariableSource {
			case VariableSourceObject:
				variableSource = data
			case VariableSourceContext:
				variableSource = ctx.Variables
			default:
				return fmt.Errorf("InputTemplate.Render: cannot resolve variable of kind: %d", i.Segments[j].VariableSource)
			}
			value, _, _, err := jsonparser.Get(variableSource, i.Segments[j].VariableSourcePath...)
			if err != nil {
				return err
			}
			_, err = preparedInput.Write(value)
			if err != nil {
				return err
			}
		}
	}
	return
}

type SegmentType int
type VariableSource int

const (
	StaticSegmentType SegmentType = iota + 1
	VariableSegmentType

	VariableSourceObject VariableSource = iota + 1
	VariableSourceContext
)

type TemplateSegment struct {
	SegmentType        SegmentType
	Data               []byte
	VariableSource     VariableSource
	VariableSourcePath []string
}

func (_ *SingleFetch) FetchKind() FetchKind {
	return FetchKindSingle
}

type ParallelFetch struct {
	Fetches []*SingleFetch
}

func (_ *ParallelFetch) FetchKind() FetchKind {
	return FetchKindParallel
}

type String struct {
	Path     []string
	Nullable bool
}

func (_ *String) NodeKind() NodeKind {
	return NodeKindString
}

type Boolean struct {
	Path     []string
	Nullable bool
}

func (_ *Boolean) NodeKind() NodeKind {
	return NodeKindBoolean
}

type Float struct {
	Path     []string
	Nullable bool
}

func (_ *Float) NodeKind() NodeKind {
	return NodeKindFloat
}

type Integer struct {
	Path     []string
	Nullable bool
}

func (_ *Integer) NodeKind() NodeKind {
	return NodeKindInteger
}

type Array struct {
	Path                []string
	Nullable            bool
	ResolveAsynchronous bool
	Item                Node
}

func (_ *Array) NodeKind() NodeKind {
	return NodeKindArray
}

type Variable interface {
	VariableKind() VariableKind
	Equals(another Variable) bool
}

type Variables []Variable

func NewVariables(variables ...Variable) Variables {
	return variables
}

const (
	variablePrefixSuffix = "$$"
	quotes               = "\""
)

func (v *Variables) AddVariable(variable Variable, quoteValue bool) (name string, exists bool) {
	index := -1
	for i := range *v {
		if (*v)[i].Equals(variable) {
			index = i
			exists = true
			break
		}
	}
	if index == -1 {
		*v = append(*v, variable)
		index = len(*v) - 1
	}
	i := strconv.Itoa(index)
	name = variablePrefixSuffix + i + variablePrefixSuffix
	if quoteValue {
		name = quotes + name + quotes
	}
	return
}

type VariableKind int

const (
	VariableKindContext VariableKind = iota + 1
	VariableKindObject
)

type ContextVariable struct {
	Path []string
}

func (c *ContextVariable) Equals(another Variable) bool {
	if another == nil {
		return false
	}
	if another.VariableKind() != c.VariableKind() {
		return false
	}
	anotherContextVariable := another.(*ContextVariable)
	if len(c.Path) != len(anotherContextVariable.Path) {
		return false
	}
	for i := range c.Path {
		if c.Path[i] != anotherContextVariable.Path[i] {
			return false
		}
	}
	return true
}

func (_ *ContextVariable) VariableKind() VariableKind {
	return VariableKindContext
}

type ObjectVariable struct {
	Path []string
}

func (o *ObjectVariable) Equals(another Variable) bool {
	if another == nil {
		return false
	}
	if another.VariableKind() != o.VariableKind() {
		return false
	}
	anotherObjectVariable := another.(*ObjectVariable)
	if len(o.Path) != len(anotherObjectVariable.Path) {
		return false
	}
	for i := range o.Path {
		if o.Path[i] != anotherObjectVariable.Path[i] {
			return false
		}
	}
	return true
}

func (o *ObjectVariable) VariableKind() VariableKind {
	return VariableKindObject
}

type GraphQLResponse struct {
	Data Node
}

type BufPair struct {
	Data   *bytes.Buffer
	Errors *bytes.Buffer
}

func NewBufPair() *BufPair {
	return &BufPair{
		Data:   &bytes.Buffer{},
		Errors: &bytes.Buffer{},
	}
}

func (b *BufPair) HasData() bool {
	return b.Data.Len() != 0
}

func (b *BufPair) HasErrors() bool {
	return b.Errors.Len() != 0
}

func (b *BufPair) Reset() {
	b.Data.Reset()
	b.Errors.Reset()
}

func (b *BufPair) writeErrors(err error, data []byte) error {
	if err != nil {
		return err
	}
	_, err = b.Errors.Write(data)
	return err
}

func (b *BufPair) WriteErr(message, locations, path []byte) (err error) {
	if b.HasErrors() {
		err = b.writeErrors(err, comma)
	}
	err = b.writeErrors(err, lBrace)
	err = b.writeErrors(err, quote)
	err = b.writeErrors(err, literalMessage)
	err = b.writeErrors(err, quote)
	err = b.writeErrors(err, colon)
	err = b.writeErrors(err, quote)
	err = b.writeErrors(err, message)
	err = b.writeErrors(err, quote)
	if locations != nil {
		err = b.writeErrors(err, comma)
		err = b.writeErrors(err, quote)
		err = b.writeErrors(err, literalLocations)
		err = b.writeErrors(err, quote)
		err = b.writeErrors(err, colon)
		err = b.writeErrors(err, locations)
	}
	if path != nil {
		err = b.writeErrors(err, comma)
		err = b.writeErrors(err, quote)
		err = b.writeErrors(err, literalPath)
		err = b.writeErrors(err, quote)
		err = b.writeErrors(err, colon)
		err = b.writeErrors(err, path)
	}
	err = b.writeErrors(err, rBrace)
	return
}

func (r *Resolver) MergeBufPairs(from, to *BufPair, prefixDataWithComma bool) (dataWritten, errorsWritten int, err error) {
	dataWritten, err = r.MergeBufPairData(from, to, prefixDataWithComma)
	if err != nil {
		return
	}
	errorsWritten, err = r.MergeBufPairErrors(from, to)
	return
}

func (r *Resolver) MergeBufPairData(from, to *BufPair, prefixDataWithComma bool) (dataWritten int, err error) {
	if !from.HasData() {
		return
	}
	var written int64
	if prefixDataWithComma {
		dataWritten, err = to.Data.Write(comma)
		if err != nil {
			return
		}
	}
	written, err = from.Data.WriteTo(to.Data)
	dataWritten += int(written)
	return
}

func (r *Resolver) MergeBufPairErrors(from, to *BufPair) (errorsWritten int, err error) {
	if !from.HasErrors() {
		return
	}
	var written int64
	if to.HasErrors() {
		errorsWritten, err = to.Errors.Write(comma)
		if err != nil {
			return
		}
	}
	written, err = from.Errors.WriteTo(to.Errors)
	errorsWritten += int(written)
	return
}

func (r *Resolver) freeBufPair(pair *BufPair) {
	pair.Data.Reset()
	pair.Errors.Reset()
	r.bufPairPool.Put(pair)
}

func (r *Resolver) getBufPair() *BufPair {
	return r.bufPairPool.Get().(*BufPair)
}

func (r *Resolver) getBufPairSlice() *[]*BufPair {
	return r.bufPairSlicePool.Get().(*[]*BufPair)
}

func (r *Resolver) freeBufPairSlice(slice *[]*BufPair) {
	for i := range *slice {
		r.freeBufPair((*slice)[i])
	}
	*slice = (*slice)[:0]
	r.bufPairSlicePool.Put(slice)
}

func (r *Resolver) getErrChan() chan error {
	return r.errChanPool.Get().(chan error)
}

func (r *Resolver) freeErrChan(ch chan error) {
	r.errChanPool.Put(ch)
}

func (r *Resolver) getWaitGroup() *sync.WaitGroup {
	return &sync.WaitGroup{}
}

func (r *Resolver) freeWaitGroup(wg *sync.WaitGroup) {
	r.waitGroupPool.Put(wg)
}

func (r *Resolver) getInflightFetch() *inflightFetch {
	return r.inflightFetchPool.Get().(*inflightFetch)
}

func (r *Resolver) freeInflightFetch(f *inflightFetch) {
	f.bufPair.Data.Reset()
	f.bufPair.Errors.Reset()
	f.err = nil
	r.inflightFetchPool.Put(f)
}
