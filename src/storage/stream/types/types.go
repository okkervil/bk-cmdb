/*
 * Tencent is pleased to support the open source community by making 蓝鲸 available.
 * Copyright (C) 2017-2018 THL A29 Limited, a Tencent company. All rights reserved.
 * Licensed under the MIT License (the "License"); you may not use this file except
 * in compliance with the License. You may obtain a copy of the License at
 * http://opensource.org/licenses/MIT
 * Unless required by applicable law or agreed to in writing, software distributed under
 * the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
 * either express or implied. See the License for the specific language governing permissions and
 * limitations under the License.
 */

package types

import (
	"errors"
	"fmt"
	"reflect"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type OperType string

const (
	// reference doc:
	// https://docs.mongodb.com/manual/reference/change-events/#change-events
	Insert       OperType = "insert"
	Delete       OperType = "delete"
	Replace      OperType = "replace"
	Update       OperType = "update"
	Drop         OperType = "drop"
	Rename       OperType = "rename"
	DropDatabase OperType = "dropDatabase"
	Invalidate   OperType = "invalidate"
	// Lister OperType is a self defined type, which is represent this operation comes from
	// a list watcher's find operations, it does not really come form the mongodb's change event.
	Lister OperType = "lister"
	// ListerDone OperType is a self defined type, which means that the list operation has already finished,
	// and the watch events starts. this OperType send only for once.
	// Note: it's only used in the ListWatch Operation.
	ListDone OperType = "listerDone"
)

type Options struct {
	// reference doc:
	// https://docs.mongodb.com/manual/reference/method/db.collection.watch/#change-stream-with-full-document-update-lookup
	// default value is true
	MajorityCommitted *bool

	// The maximum amount of time in milliseconds the server waits for new
	// data changes to report to the change stream cursor before returning
	// an empty batch.
	// default value is 1000ms
	MaxAwaitTime *time.Duration

	// OperationType describe which kind of operation you want to watch,
	// such as a "insert" operation or a "replace" operation.
	// If you don't set, it will means watch  all kinds of operations.
	OperationType *OperType

	// Filter helps you filter out which kind of data's change event you want
	// to receive, such as the filter :
	// {"bk_obj_id":"biz"} means you can only receives the data that has this kv.
	// Note: the filter's key must be a exist document key filed in the collection's
	// document
	Filter map[string]interface{}

	// EventStruct is the point data struct that the event decoded into.
	// Note: must be a point value.
	EventStruct interface{}

	// Collection defines which collection you want you watch.
	Collection string
}

var defaultMaxAwaitTime = time.Second

// CheckSet check the legal of each option, and set the default value
func (opts *Options) CheckSetDefault() error {
	if reflect.ValueOf(opts.EventStruct).Kind() != reflect.Ptr ||
		reflect.ValueOf(opts.EventStruct).IsNil() {
		fmt.Errorf("invalid EventStruct field, must be a none pointer")
	}

	if opts.MajorityCommitted == nil {
		t := true
		opts.MajorityCommitted = &t
	}

	if opts.MaxAwaitTime == nil {
		opts.MaxAwaitTime = &defaultMaxAwaitTime
	}

	if len(opts.Collection) == 0 {
		return errors.New("invalid Namespace field, database and collection can not be empty")
	}
	return nil
}

type WatchOptions struct {
	Options
	// StartAfterToken describe where you want to watch the event.
	// Note: the returned event does'nt contains the token represented,
	// and will returns event just after this token.
	StartAfterToken *EventToken
}

var defaultListPageSize = 1000

type ListWatchOptions struct {
	Options

	// Step defines the list step when the client try to list all the data defines in the
	// namespace. default value is `DefaultListStep`, value range [200,2000]
	PageSize *int
}

func (lw *ListWatchOptions) CheckSetDefault() error {
	if err := lw.Options.CheckSetDefault(); err != nil {
		return err
	}

	if lw.PageSize != nil {
		if *lw.PageSize < 0 || *lw.PageSize > 2000 {
			return fmt.Errorf("invalid page size, range is [200,2000]")
		}
	} else {
		lw.PageSize = &defaultListPageSize
	}

	return nil
}

const DefaultEventChanSize = 100

type Watcher struct {
	EventChan <-chan *Event
}

type Event struct {
	// Oid represent the unique document key filed "_id"
	Oid           string
	Document      interface{}
	OperationType OperType
}

// mongodb change stream token, which represent a event's identity.
type EventToken struct {
	// Hex value of document's _id
	Data string `bson:"_data"`
}

// reference:
// https://docs.mongodb.com/manual/reference/change-events/
type EventStream struct {
	Token         EventToken          `bson:"_id"`
	OperationType OperType            `bson:"operationType"`
	ClusterTime   primitive.Timestamp `bson:"clusterTime"`
	Namespace     Namespace           `bson:"ns"`
	DocumentKey   Key                 `bson:"documentKey"`
}

type Key struct {
	// the unique document id, as is "_id"
	ID primitive.ObjectID `bson:"_id"`
}

type Namespace struct {
	Database   string `bson:"db"`
	Collection string `bson:"coll"`
}
