/*
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements.  See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License.  You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package dubbod

import (
	"fmt"
	registry "github.com/mosn/registry/dubbo"
	dubbocommon "github.com/mosn/registry/dubbo/common"
	dubboconsts "github.com/mosn/registry/dubbo/common/constant"
	v2 "mosn.io/mosn/pkg/config/v2"
	"mosn.io/mosn/pkg/log"
	routerAdapter "mosn.io/mosn/pkg/router"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// map[string]registry.NotifyListener{}
var dubboInterface2listener = sync.Map{}

// inject a router to router manager
// which name is "dubbo"
func initRouterManager() {
	// this can also be put into mosn's config json file
	err := routerAdapter.GetRoutersMangerInstance().AddOrUpdateRouters(&v2.RouterConfiguration{
		RouterConfigurationConfig: v2.RouterConfigurationConfig{
			RouterConfigName: dubboRouterConfigName,
		},
		VirtualHosts: []*v2.VirtualHost{
			{
				Name:    dubboRouterConfigName,
				Domains: []string{"*"},
			},
		},
	})
	if err != nil {
		log.DefaultLogger.Fatalf("auto write config when updated")
	}
}

// subscribe a service from registry
func subscribe(w http.ResponseWriter, r *http.Request) {
	var req subReq
	err := bind(r, &req)
	if err != nil {
		response(w, resp{Errno: fail, ErrMsg: "subscribe fail, err: " + err.Error()})
		return
	}

	var registryPath = registryPathTpl.ExecuteString(map[string]interface{}{
		"addr": req.Registry.Addr,
	})
	registryURL, _ := dubbocommon.NewURL(registryPath,
		dubbocommon.WithParams(url.Values{
			dubboconsts.REGISTRY_TIMEOUT_KEY: []string{"5s"},
		}),
		dubbocommon.WithUsername(req.Registry.UserName),
		dubbocommon.WithPassword(req.Registry.Password),
	)

	servicePath := req.Service.Interface // com.mosn.test.UserService
	reg, err := getRegistry(servicePath, dubbocommon.CONSUMER, registryURL)
	if err != nil {
		response(w, resp{Errno: fail, ErrMsg: "subscribe fail, err: " + err.Error()})
		return
	}

	dubboURL := dubbocommon.NewURLWithOptions(
		dubbocommon.WithPath(servicePath),
		dubbocommon.WithProtocol("dubbo"), // this protocol is used to compare the url, must provide
		dubbocommon.WithParams(url.Values{
			dubboconsts.TIMESTAMP_KEY: []string{fmt.Sprint(time.Now().Unix())},
			dubboconsts.ROLE_KEY:      []string{fmt.Sprint(dubbocommon.CONSUMER)},
			dubboconsts.GROUP_KEY:     []string{req.Service.Group},
		}),
		dubbocommon.WithMethods(req.Service.Methods))

	// register consumer to registry
	err = reg.Register(*dubboURL)
	if err != nil {
		response(w, resp{Errno: fail, ErrMsg: "subscribe fail, err: " + err.Error()})
		return
	}

	// listen to provider change events
	var l = &listener{}
	go reg.Subscribe(dubboURL, l)
	dubboInterface2listener.Store(servicePath, l)

	err = addRouteRule(servicePath)
	if err != nil {
		response(w, resp{Errno: fail, ErrMsg: "subscribe fail, err: " + err.Error()})
		return
	}

	response(w, resp{Errno: succ, ErrMsg: "subscribe success"})
}

// unsubscribe a service from registry
func unsubscribe(w http.ResponseWriter, r *http.Request) {
	var req unsubReq
	err := bind(r, &req)
	if err != nil {
		response(w, resp{Errno: fail, ErrMsg: "unsubscribe fail, err: " + err.Error()})
		return
	}

	var registryPath = registryPathTpl.ExecuteString(map[string]interface{}{
		"addr": req.Registry.Addr,
	})
	registryURL, _ := dubbocommon.NewURL(registryPath,
		dubbocommon.WithParams(url.Values{
			dubboconsts.REGISTRY_TIMEOUT_KEY: []string{"5s"},
		}),
		dubbocommon.WithUsername(req.Registry.UserName),
		dubbocommon.WithPassword(req.Registry.Password),
	)

	servicePath := req.Service.Interface // com.mosn.test.UserService
	reg, err := getRegistry(servicePath, dubbocommon.CONSUMER, registryURL)
	if err != nil {
		response(w, resp{Errno: fail, ErrMsg: "unsubscribe fail, err: " + err.Error()})
		return
	}

	dubboURL := dubbocommon.NewURLWithOptions(
		dubbocommon.WithPath(servicePath),
		dubbocommon.WithProtocol("dubbo"), // this protocol is used to compare the url, must provide
		dubbocommon.WithParams(url.Values{
			dubboconsts.TIMESTAMP_KEY: []string{fmt.Sprint(time.Now().Unix())},
			dubboconsts.ROLE_KEY:      []string{fmt.Sprint(dubbocommon.CONSUMER)},
			dubboconsts.GROUP_KEY:     []string{req.Service.Group},
		}),
		dubbocommon.WithMethods(req.Service.Methods))

	// unregister consumer
	err = reg.UnRegister(*dubboURL)
	if err != nil {
		response(w, resp{Errno: fail, ErrMsg: "unsubscribe fail, err: " + err.Error()})
		return
	}

	l, ok := dubboInterface2listener.Load(servicePath)
	if ok {
		err = reg.UnSubscribe(dubboURL, l.(registry.NotifyListener))
	}

	if err != nil {
		response(w, resp{Errno: fail, ErrMsg: "unsubscribe fail, err: " + err.Error()})
		return
	}

	response(w, resp{Errno: succ, ErrMsg: "unsubscribe success"})
}

var dubboInterface2registerFlag = sync.Map{}
func addRouteRule(servicePath string) error {
	// if already route rule of this service is already added to router manager
	// then skip
	if _, ok := dubboInterface2registerFlag.Load(servicePath); ok {
		return nil
	}

	dubboInterface2registerFlag.Store(servicePath, struct{}{})
	return routerAdapter.GetRoutersMangerInstance().AddRoute(dubboRouterConfigName, "*", &v2.Router{
		RouterConfig: v2.RouterConfig{
			Match: v2.RouterMatch{
				Headers: []v2.HeaderMatcher{
					{
						Name:  "service", // use the xprotocol header field "service"
						Value: servicePath,
					},
				},
			},
			Route: v2.RouteAction{
				RouterActionConfig: v2.RouterActionConfig{
					ClusterName: servicePath,
				},
			},
		},
	})
}