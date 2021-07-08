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

package registerdiscover

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"configcenter/src/common/backbone/service_mange/zk"
	"configcenter/src/common/blog"
	"configcenter/src/common/zkclient"

	gozk "github.com/samuel/go-zookeeper/zk"
)

// ZkRegDiscv do register and discover by zookeeper
type ZkRegDiscv struct {
	zkcli          *zkclient.ZkClient
	cancel         context.CancelFunc
	rootCxt        context.Context
	sessionTimeOut time.Duration
	sync.Mutex
	// registeredPathsMap is all registered paths and it's concurrent secure
	registeredPathsMap sync.Map
}

// NewZkRegDiscv create a object of ZkRegDiscv
func NewZkRegDiscv(client *zk.ZkClient) *ZkRegDiscv {
	ctx, ctxCancel := client.WithCancel()
	return &ZkRegDiscv{
		zkcli:          client.Client(),
		sessionTimeOut: client.SessionTimeOut(),
		cancel:         ctxCancel,
		rootCxt:        ctx,
	}
}

// pathMgr a path manager to operator on register path and it's concurrent secure
type pathMgr struct {
	sync.RWMutex
	registerPath string
}

// NewPathMgr new a path manager
func NewPathMgr(path string) *pathMgr {
	return &pathMgr{
		registerPath: path,
	}
}

// getRegisterPath get register path and and it's concurrent secure
func (m *pathMgr) getRegisterPath() string {
	m.RLock()
	defer m.RUnlock()
	return m.registerPath
}

// setRegisterPath set register path and and it's concurrent secure
func (m *pathMgr) setRegisterPath(path string) {
	m.Lock()
	defer m.Unlock()
	m.registerPath = path
}

// RegisterAndWatch create ephemeral node for the service and watch it. if it exit, register again
func (zkRD *ZkRegDiscv) RegisterAndWatch(path string, data []byte) error {
	blog.Infof("register server and watch it. path(%s), data(%s)", path, string(data))
	pMgr := NewPathMgr("")

	go func(pathMgr *pathMgr) {
		watchCtx := zkRD.rootCxt
		var exist bool
		for {
			var watchEvn <-chan gozk.Event
			var err error

			if pathMgr.getRegisterPath() == "" {
				registerPath, err := zkRD.zkcli.CreateEphAndSeqEx(path, data)
				if err != nil {
					blog.Errorf("fail to register server node(%s). CreateEphAndSeqEx err:%s", path, err.Error())
					if zkRD.zkcli.IsConnectionError(err) {
						blog.Error("zk is closed, try to reconnect")
						zkRD.reconnectZk()
						continue
					}
					time.Sleep(time.Second)
				}
				pathMgr.setRegisterPath(registerPath)
				zkRD.registeredPathsMap.Store(registerPath, struct{}{})
				// continue retry watch node
				continue
			} else {
				registerPath := pathMgr.getRegisterPath()
				exist, _, watchEvn, err = zkRD.zkcli.ExistW(registerPath)
				if err != nil {
					blog.Errorf("fail to watch register node(%s), err:%s\n", registerPath, err.Error())
					if zkRD.zkcli.IsConnectionError(err) {
						blog.Error("zk is closed, try to reconnect")
						zkRD.reconnectZk()
						continue
					}

					switch err {
					default:
						// clear register path, so that it can register to a new path
						zkRD.zkcli.Del(registerPath, -1)
						pathMgr.setRegisterPath("")
						zkRD.registeredPathsMap.Delete(registerPath)
						// err still exists, waiting 1s. avoid too quick retry.
						time.Sleep(time.Second * 1)
						continue
					}
				}
				if !exist {
					// current node doesn't exist, reset the path to create a new one
					blog.Errorf("node %s doesn't exist, try to create a new one", registerPath)
					pathMgr.setRegisterPath("")
					zkRD.registeredPathsMap.Delete(registerPath)
					continue
				}
			}

			select {
			case <-watchCtx.Done():
				blog.Infof("watch register node(%s) done, now exist service register.\n", path)
				return
			case e := <-watchEvn:
				blog.Infof("watch register node(%s) exist changed, event(%v)\n", path, e)
				if e.State == gozk.StateDisconnected {
					zkRD.reconnectZk()
				}
				continue
			}
		}
	}(pMgr)

	go zkRD.loopRegisterNode(pMgr, path, data)

	blog.Infof("finish register server node(%s) and watch it\n", path)
	return nil
}

// loopRegisterNode loop check registered node and register it again if doesn't exist on zk
// as a standby, in case watch encountered error or other register exception
func (zkRD *ZkRegDiscv) loopRegisterNode(pathMgr *pathMgr, path string, data []byte) {
	var exist bool
	var err error
	for {
		// sleep some time for every loop
		time.Sleep(time.Second)

		registerPath := pathMgr.getRegisterPath()
		if registerPath != "" {
			// check whether the node exists
			exist, err = zkRD.zkcli.Exist(registerPath)
			if err != nil {
				blog.Errorf("fail to get zk node exist status, register path:%s, err:%s\n",
					registerPath, err.Error())
				if zkRD.zkcli.IsConnectionError(err) {
					blog.Error("zk is closed, try to reconnect")
					zkRD.reconnectZk()
					continue
				}
			}

			// if it exists，continue the loop, else delete the old path and create a new one
			if exist {
				continue
			} else {
				zkRD.registeredPathsMap.Delete(registerPath)
			}
		}

		// if the register path is empty or it doesn't exist， create it
		blog.Infof("loop register node at path:%s", path)
		registerPath, err := zkRD.zkcli.CreateEphAndSeqEx(path, data)
		if err != nil {
			blog.Errorf("fail to register server node(%s). CreateEphAndSeqEx err:%s", path, err.Error())
			if zkRD.zkcli.IsConnectionError(err) {
				blog.Error("zk is closed, try to reconnect")
				zkRD.reconnectZk()
				continue
			}
		}
		pathMgr.setRegisterPath(registerPath)
		zkRD.registeredPathsMap.Store(registerPath, struct{}{})
	}
}

// GetServNodes get server nodes by path
func (zkRD *ZkRegDiscv) GetServNodes(path string) ([]string, error) {
	return zkRD.zkcli.GetChildren(path)
}

// Ping to ping server
func (zkRD *ZkRegDiscv) Ping() error {
	return zkRD.zkcli.Ping()
}

// Discover watch the children
func (zkRD *ZkRegDiscv) Discover(path string) (<-chan *DiscoverEvent, error) {
	fmt.Printf("begin to discover by watch children of path(%s)\n", path)
	discvCtx := zkRD.rootCxt

	env := make(chan *DiscoverEvent, 1)

	go zkRD.loopDiscover(discvCtx, path, env)

	// loop compare server info in case watch encountered error
	go func() {
		var oldServer map[string]bool
		for {
			event := zkRD.getServerInfoByPath(path)
			isUpdated := false
			newServer := make(map[string]bool)
			if len(event.Server) != len(oldServer) {
				isUpdated = true
			}
			for _, server := range event.Server {
				if !isUpdated && !oldServer[server] {
					isUpdated = true
				}
				newServer[server] = true
			}
			oldServer = newServer
			if isUpdated {
				env <- event
			}
			select {
			case <-discvCtx.Done():
				return
			default:
				time.Sleep(time.Second)
			}
		}
	}()

	return env, nil
}

func (zkRD *ZkRegDiscv) loopDiscover(discvCtx context.Context, path string, env chan *DiscoverEvent) {
	for {
		_, watchEnv, err := zkRD.zkcli.WatchChildren(path)
		if err != nil {
			fmt.Printf("fail to watch children for path(%s), err:%s\n", path, err.Error())
			if zkclient.ErrNoNode == err {
				fmt.Printf("children node(%s) is not exist, will watch after 5s\n", path)
				time.Sleep(5 * time.Second)
				continue
			}
			if zkRD.zkcli.IsConnectionError(err) {
				blog.Error("zk is closed, try to reconnect")
				zkRD.reconnectZk()
				continue
			}

			fmt.Printf("unknow err accur when watch children:%v,will watch after 10s", err)
			time.Sleep(10 * time.Second)
			continue
		}

		// write into discoverEvent channel
		env <- zkRD.getServerInfoByPath(path)

		select {
		case <-discvCtx.Done():
			fmt.Printf("discover path(%s) done\n", path)
			return
		case e := <-watchEnv:
			fmt.Printf("watch found the children of path(%s) change. event type:%s, event err:%v\n",
				path, e.Type.String(), e.Err)
			if e.State == gozk.StateDisconnected {
				zkRD.reconnectZk()
			}
		}
	}
}

func (zkRD *ZkRegDiscv) getServerInfoByPath(path string) *DiscoverEvent {
	for {
		discvEnv := &DiscoverEvent{
			Err: nil,
			Key: path,
		}
		servNodes, err := zkRD.zkcli.GetChildren(path)
		if err != nil {
			if zkclient.ErrNoNode == err {
				fmt.Printf("children node(%s) is not exist, will watch after 5s\n", path)
				time.Sleep(5 * time.Second)
				continue
			}
			if zkRD.zkcli.IsConnectionError(err) {
				blog.Error("zk is closed, try to reconnect")
				zkRD.reconnectZk()
				continue
			}
			fmt.Printf("get children node(%s) error:%v,will watch after 10s\n", path, err)
			time.Sleep(10 * time.Second)
			continue
		}
		discvEnv.Nodes = append(discvEnv.Nodes, servNodes...)
		// sort server node
		servNodes = zkRD.sortNode(servNodes)

		isGetNodeInfoErr := false
		// get server info
		for _, node := range servNodes {
			servPath := path + "/" + node
			servInfo, err := zkRD.zkcli.Get(servPath)
			if err != nil {
				if err == zkclient.ErrNodeExists {
					continue
				}
				if zkRD.zkcli.IsConnectionError(err) {
					blog.Error("zk is closed, try to reconnect")
					zkRD.reconnectZk()
					isGetNodeInfoErr = true
					break
				}
				isGetNodeInfoErr = true
				fmt.Printf("fail to get server info from zookeeper by path(%s), err:%s\n", servPath, err.Error())
				break
			}

			discvEnv.Server = append(discvEnv.Server, servInfo)
		}
		if isGetNodeInfoErr {
			continue
		}

		return discvEnv
	}

}

// reconnectZk try to reconnect zookeeper until success
func (zkRD *ZkRegDiscv) reconnectZk() {
	zkRD.Lock()
	defer zkRD.Unlock()

	for {
		if zkRD.zkcli.ZkConn != nil && zkRD.zkcli.Ping() == nil {
			blog.Info("connection is healthy, there is no need to reconnect, **skip**")
			return
		}

		if err := zkRD.zkcli.ConnectEx(zkRD.sessionTimeOut); err != nil {
			fmt.Printf("reconnect zookeeper error:%v, will try connect after 5s\n", err)
			time.Sleep(5 * time.Second)
			continue
		}
		// wait some time to make the new connection available
		time.Sleep(time.Second)
		fmt.Println("reconnect zookeeper success")

		return
	}
}

func (zkRD *ZkRegDiscv) sortNode(nodes []string) []string {
	var sortPart []int
	mapSortNode := make(map[int]string)
	for _, chNode := range nodes {
		if len(chNode) <= 10 {
			fmt.Printf("node(%s) is less then 10, there is not the seq number\n", chNode)
			continue
		}

		p, err := strconv.Atoi(chNode[len(chNode)-10:])
		if err != nil {
			fmt.Printf("fail to conv string to seq number for node(%s), err:%s\n", chNode, err.Error())
			continue
		}

		sortPart = append(sortPart, p)
		mapSortNode[p] = chNode
	}

	sort.Ints(sortPart)

	var children []string
	for _, part := range sortPart {
		children = append(children, mapSortNode[part])
	}

	return children
}

// Cancel to stop server register and discover
func (zkRD *ZkRegDiscv) Cancel() {
	zkRD.cancel()
}

// ClearRegisterPath to delete server register path from zk
func (zkRD *ZkRegDiscv) ClearRegisterPath() error {
	var err error
	zkRD.registeredPathsMap.Range(func(k, v interface{}) bool {
		if err = zkRD.zkcli.Del(k.(string), -1); err != nil {
			return false
		}
		return true
	})
	return err
}
