package channeld

import (
	"container/list"
	"fmt"
	"math/rand"
	"net"
	"strconv"
	"testing"
	"time"

	"channeld.clewcat.com/channeld/pkg/channeldpb"
	"github.com/indiest/fmutils"
	"github.com/stretchr/testify/assert"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

type testQueuedMessageSender struct {
	MessageSender
	msgQueue     []Message
	msgProcessor func(Message) (Message, error)
}

func (s *testQueuedMessageSender) Send(c *Connection, ctx MessageContext) {
	if s.msgProcessor != nil {
		var err error
		ctx.Msg, err = s.msgProcessor(ctx.Msg)
		if err != nil {
			panic(err)
		}
	}
	s.msgQueue = append(s.msgQueue, ctx.Msg)
}

func addTestConnection(t channeldpb.ConnectionType) *Connection {
	return addTestConnectionWithProcessor(t, nil)
}

func addTestConnectionWithProcessor(t channeldpb.ConnectionType, p func(Message) (Message, error)) *Connection {
	conn1, _ := net.Pipe()
	c := AddConnection(conn1, t)
	c.sender = &testQueuedMessageSender{msgQueue: make([]Message, 0), msgProcessor: p}
	return c
}

func (c *Connection) testQueue() []Message {
	return c.sender.(*testQueuedMessageSender).msgQueue
}

func (c *Connection) latestMsg() Message {
	queue := c.testQueue()
	if len(queue) > 0 {
		return queue[len(queue)-1]
	} else {
		return nil
	}
}

func testChannelDataMessageProcessor(msg Message) (Message, error) {
	// Extract the payload from the ChannelDataUpdatMessage
	payload := msg.(*channeldpb.ChannelDataUpdateMessage).Data
	updateMsg, err := payload.UnmarshalNew()
	return updateMsg, err
}

// See the test case in [the design doc](doc/design.md#fan-out)
// TODO: add test cases with FieldMasks (no fan-out if no property is updated)
func TestFanOutChannelData(t *testing.T) {
	InitLogsAndMetrics()
	InitChannels()

	c0 := addTestConnectionWithProcessor(channeldpb.ConnectionType_SERVER, testChannelDataMessageProcessor)
	c1 := addTestConnectionWithProcessor(channeldpb.ConnectionType_CLIENT, testChannelDataMessageProcessor)
	c2 := addTestConnectionWithProcessor(channeldpb.ConnectionType_CLIENT, testChannelDataMessageProcessor)

	testChannel, _ := CreateChannel(channeldpb.ChannelType_TEST, c0)
	dataMsg := &channeldpb.TestChannelDataMessage{
		Text: "a",
		Num:  1,
	}
	testChannel.InitData(dataMsg, nil)
	// We need to manually tick the channel. Set the interval to a very large value.
	testChannel.tickInterval = time.Hour

	c0.SubscribeToChannel(testChannel, nil)
	c1.SubscribeToChannel(testChannel, &channeldpb.ChannelSubscriptionOptions{
		FanOutIntervalMs: 50,
	})

	channelStartTime := ChannelTime(100 * int64(time.Millisecond))
	// F0 = the whole data
	testChannel.tickData(channelStartTime)
	assert.Equal(t, 1, len(c1.testQueue()))
	assert.Equal(t, 0, len(c2.testQueue()))
	assert.EqualValues(t, dataMsg.Num, c1.latestMsg().(*channeldpb.TestChannelDataMessage).Num)

	c2.SubscribeToChannel(testChannel, &channeldpb.ChannelSubscriptionOptions{
		FanOutIntervalMs: 100,
	})
	// F1 = no data, F7 = the whole data
	testChannel.tickData(channelStartTime.AddMs(50))
	assert.Equal(t, 1, len(c1.testQueue()))
	assert.Equal(t, 1, len(c2.testQueue()))
	assert.EqualValues(t, dataMsg.Num, c2.latestMsg().(*channeldpb.TestChannelDataMessage).Num)

	// U1 arrives
	u1 := &channeldpb.TestChannelDataMessage{Text: "b"}
	testChannel.Data().OnUpdate(u1, channelStartTime.AddMs(60), nil)

	// F2 = U1
	testChannel.tickData(channelStartTime.AddMs(100))
	assert.Equal(t, 2, len(c1.testQueue()))
	assert.Equal(t, 1, len(c2.testQueue()))
	// U1 doesn't have "ClientConnNum" property
	assert.NotEqualValues(t, dataMsg.Num, c1.latestMsg().(*channeldpb.TestChannelDataMessage).Num)
	assert.EqualValues(t, "b", c1.latestMsg().(*channeldpb.TestChannelDataMessage).Text)
	assert.EqualValues(t, "a", c2.latestMsg().(*channeldpb.TestChannelDataMessage).Text)

	// U2 arrives
	u2 := &channeldpb.TestChannelDataMessage{Text: "c"}
	testChannel.Data().OnUpdate(u2, channelStartTime.AddMs(120), nil)

	// F8=U1+U2; F3 = U2
	testChannel.tickData(channelStartTime.AddMs(150))
	assert.Equal(t, 3, len(c1.testQueue()))
	assert.Equal(t, 2, len(c2.testQueue()))
	assert.EqualValues(t, "c", c1.latestMsg().(*channeldpb.TestChannelDataMessage).Text)
	assert.EqualValues(t, "c", c2.latestMsg().(*channeldpb.TestChannelDataMessage).Text)
}

func BenchmarkCustomMergeMap(b *testing.B) {
	dst := &channeldpb.TestMergeMessage{
		Kv: map[int64]*channeldpb.TestMergeMessage_StringWrapper{},
	}
	src := &channeldpb.TestMergeMessage{
		Kv: map[int64]*channeldpb.TestMergeMessage_StringWrapper{},
	}
	for i := 0; i < 100; i++ {
		dst.Kv[int64(i)] = &channeldpb.TestMergeMessage_StringWrapper{Removed: false, Content: strconv.Itoa(rand.Int())}
		if rand.Intn(100) < 10 {
			src.Kv[int64(i)] = &channeldpb.TestMergeMessage_StringWrapper{Removed: true}
		} else {
			src.Kv[int64(i)] = &channeldpb.TestMergeMessage_StringWrapper{Removed: false, Content: strconv.Itoa(rand.Int())}
		}
	}

	mergeOptions := &channeldpb.ChannelDataMergeOptions{ShouldCheckRemovableMapField: true}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mergeWithOptions(dst, src, mergeOptions, nil)
	}

	// Protoreflect merge:
	// BenchmarkCustomMergeMap-12    	   26959	     43900 ns/op	    8464 B/op	     316 allocs/op
	// BenchmarkCustomMergeMap-12    	   27038	     46457 ns/op	    8464 B/op	     316 allocs/op
	// BenchmarkCustomMergeMap-12    	   24746	     49732 ns/op	    8464 B/op	     316 allocs/op

	// Custom merge: (15x faster!!!)
	// BenchmarkCustomMergeMap-12    	  353163	      3172 ns/op	       0 B/op	       0 allocs/op
	// BenchmarkCustomMergeMap-12    	  457196	      2871 ns/op	       0 B/op	       0 allocs/op
	// BenchmarkCustomMergeMap-12    	  419090	      3004 ns/op	       0 B/op	       0 allocs/op
}

func TestListRemoveElement(t *testing.T) {
	list := list.New()
	list.PushBack("a")
	list.PushBack("b")
	list.PushBack("b")
	list.PushBack("c")
	list.PushBack("b")
	list.PushBack("d")
	p := list.Front()
	var n int = list.Len()
	for i := 0; i < n; i++ {
		fmt.Println(p.Value)
		if p.Value == "b" {
			tmp := p.Next()
			list.Remove(p)
			p = tmp
			continue
		}
		p = p.Next()
	}
	assert.Equal(t, 3, list.Len())
}

func TestListMoveElement(t *testing.T) {
	list := list.New()
	list.PushBack("a")
	list.PushBack("b")
	list.PushBack("c")
	assert.Equal(t, "a", list.Front().Value)

	e := list.Front().Next()
	assert.Equal(t, "b", e.Value)
	temp := e.Prev()
	list.MoveToBack(e)
	e = temp.Next()
	assert.Equal(t, "c", e.Value)
}

func TestDataMergeOptions(t *testing.T) {
	InitLogsAndMetrics()
	dstMsg := &channeldpb.TestMergeMessage{
		List: []string{"a", "b", "c"},
		Kv: map[int64]*channeldpb.TestMergeMessage_StringWrapper{
			1: {Content: "aa"},
			2: {Content: "bb"},
		},
	}

	srcMsg := &channeldpb.TestMergeMessage{
		List: []string{"d", "e"},
		Kv: map[int64]*channeldpb.TestMergeMessage_StringWrapper{
			1: {Removed: true},
			2: {Content: "bbb"},
		},
	}

	mergedMsg1 := proto.Clone(dstMsg).(*channeldpb.TestMergeMessage)
	mergeOptions1 := &channeldpb.ChannelDataMergeOptions{
		ShouldReplaceList: true,
	}
	mergeWithOptions(mergedMsg1, srcMsg, mergeOptions1, nil)
	assert.Equal(t, 2, len(mergedMsg1.List))
	assert.Equal(t, "e", mergedMsg1.List[1])

	mergedMsg2 := proto.Clone(dstMsg).(*channeldpb.TestMergeMessage)
	mergeOptions2 := &channeldpb.ChannelDataMergeOptions{
		ListSizeLimit: 4,
	}
	mergeWithOptions(mergedMsg2, srcMsg, mergeOptions2, nil) // [a,b,c,d]
	assert.Equal(t, 4, len(mergedMsg2.List))
	assert.Equal(t, "d", mergedMsg2.List[3])
	mergeOptions2.TruncateTop = true
	mergeWithOptions(mergedMsg2, srcMsg, mergeOptions2, nil) // [c,d,d,e]
	assert.Equal(t, "c", mergedMsg2.List[0])
	assert.Equal(t, "e", mergedMsg2.List[3])

	mergedMsg3 := proto.Clone(dstMsg).(*channeldpb.TestMergeMessage)
	mergeOptions3 := &channeldpb.ChannelDataMergeOptions{
		ShouldCheckRemovableMapField: true,
	}
	srcBytes, _ := proto.Marshal(srcMsg)
	proto.Unmarshal(srcBytes, srcMsg)
	mergeWithOptions(mergedMsg3, srcMsg, mergeOptions3, nil)
	assert.Equal(t, 1, len(mergedMsg3.Kv))
	_, exists := mergedMsg3.Kv[1]
	assert.False(t, exists)
	assert.Equal(t, "bbb", mergedMsg3.Kv[2].Content)
}

func TestReflectChannelData(t *testing.T) {
	globalData, err := ReflectChannelData(channeldpb.ChannelType_TEST, nil)
	assert.NoError(t, err)
	assert.NotNil(t, globalData)
	assert.IsType(t, &channeldpb.TestChannelDataMessage{}, globalData.msg)
}

func TestDataFieldMasks(t *testing.T) {
	nestedMsg := &channeldpb.TestFieldMaskMessage_NestedMessage{
		P1: 1,
		P2: 2,
	}
	testMsg := &channeldpb.TestFieldMaskMessage{
		Name: "test",
		Msg:  nestedMsg,
		List: []*channeldpb.TestFieldMaskMessage_NestedMessage{nestedMsg},
		Kv1: map[int64]*channeldpb.TestFieldMaskMessage_NestedMessage{
			10: nestedMsg,
		},
		Kv2: map[int64]string{
			100: "hello",
		},
	}

	filteredMsg1 := proto.Clone(testMsg)
	fmutils.Filter(filteredMsg1, []string{"name"})
	t.Log(filteredMsg1.(*channeldpb.TestFieldMaskMessage).String())

	filteredMsg2 := proto.Clone(testMsg)
	fmutils.Filter(filteredMsg2, []string{"msg.p1"})
	t.Log(filteredMsg2.(*channeldpb.TestFieldMaskMessage).String())

	filteredMsg3 := proto.Clone(testMsg)
	fmutils.Filter(filteredMsg3, []string{"list.p2"})
	t.Log(filteredMsg3.(*channeldpb.TestFieldMaskMessage).String())

	filteredMsg4 := proto.Clone(testMsg)
	fmutils.Filter(filteredMsg4, []string{"kv1.p1", "kv1.p2", "kv1.p3"})
	t.Log(filteredMsg4.(*channeldpb.TestFieldMaskMessage).String())
	fmutils.Prune(filteredMsg4, []string{"kv1.p1"})
	t.Log(filteredMsg4.(*channeldpb.TestFieldMaskMessage).String())

	filteredMsg5 := proto.Clone(testMsg)
	fmutils.Filter(filteredMsg5, []string{"kv2.a"})
	t.Log(filteredMsg5.(*channeldpb.TestFieldMaskMessage).String())
}

func TestProtobufAny(t *testing.T) {
	any1, err := anypb.New(&channeldpb.TestAnyMessage_Type1{Value: "a"})
	assert.NoError(t, err)

	any2, err := anypb.New(&channeldpb.TestAnyMessage_Type2{Value: 1})
	assert.NoError(t, err)

	msg1 := &channeldpb.TestAnyMessage{Msg: any1}
	msg2 := &channeldpb.TestAnyMessage{Msg: any2}
	// Can merge the any property from different type
	proto.Merge(msg1, msg2)
	assert.EqualValues(t, any2, msg1.Msg)
	// Can be converted to a message of a unknown type
	um, err := msg1.Msg.UnmarshalNew()
	assert.NoError(t, err)
	assert.EqualValues(t, 1, um.(*channeldpb.TestAnyMessage_Type2).Value)

	msg1.List = append(msg1.List, any1)
	msg2.List = append(msg2.List, any2)
	// Can merge the any list of different types
	proto.Merge(msg1, msg2)
	assert.Equal(t, 2, len(msg1.List))
}

func TestProtobufMapMerge(t *testing.T) {
	testMsg := &channeldpb.TestMapMessage{
		Kv:  make(map[uint32]string),
		Kv2: make(map[uint32]*channeldpb.TestMapMessage_StringWrapper),
	}
	testMsg.Kv[1] = "a"
	testMsg.Kv[2] = "b"
	testMsg.Kv[3] = "c"
	testMsg.Kv[4] = "d"

	testMsg.Kv2[1] = &channeldpb.TestMapMessage_StringWrapper{Content: "a"}
	testMsg.Kv2[2] = &channeldpb.TestMapMessage_StringWrapper{Content: "b", Num: 2}

	updateMsg := &channeldpb.TestMapMessage{
		Kv:  make(map[uint32]string),
		Kv2: make(map[uint32]*channeldpb.TestMapMessage_StringWrapper),
	}
	updateMsg.Kv[2] = "bb"
	updateMsg.Kv[3] = ""
	updateMsg.Kv[4] = "dd"

	updateMsg.Kv2[1] = nil

	proto.Merge(testMsg, updateMsg)

	assert.Equal(t, "a", testMsg.Kv[1])
	assert.Equal(t, "bb", testMsg.Kv[2])
	assert.Equal(t, "", testMsg.Kv[3])
	assert.Equal(t, "dd", testMsg.Kv[4])

	/* By default, protobuf ignores the nil value
	assert.Equal(t, nil, testMsg.Kv2[1])
	*/
	assert.NotEqual(t, nil, testMsg.Kv2[1])
	assert.Equal(t, "b", testMsg.Kv2[2].Content)
	// The other properties should remain the same
	assert.Equal(t, int64(2), testMsg.Kv2[2].Num)
}
