/*
 * Copyright (c) 2024 Yunshan Networks
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package ctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"sort"
	"strconv"
	"time"

	. "encoding/binary"
	"github.com/deepflowio/deepflow/message/trident"
	"github.com/deepflowio/deepflow/server/libs/utils"
	"github.com/golang/protobuf/proto"
	"github.com/spf13/cobra"
	_ "golang.org/x/net/context"
	"google.golang.org/grpc"

	"github.com/deepflowio/deepflow/cli/ctl/common"
)

type ParamData struct {
	CtrlIP     string
	CtrlMac    string
	GroupID    string
	ClusterID  string
	TeamID     string
	RpcIP      string
	RpcPort    string
	Type       string
	PluginType string
	PluginName string
	OrgID      uint32
}

type SortedAcls []*trident.FlowAcl

var paramData ParamData

type CmdExecute func(response *trident.SyncResponse)

func regiterCommand() []*cobra.Command {
	platformDataCmd := &cobra.Command{
		Use:   "platform-data",
		Short: "get platform-data from deepflow-server",
		Run: func(cmd *cobra.Command, args []string) {
			initCmd(cmd, []CmdExecute{platformData})
		},
	}
	ipGroupsCmd := &cobra.Command{
		Use:   "ip-groups",
		Short: "get ip groups from deepflow-servr",
		Run: func(cmd *cobra.Command, args []string) {
			initCmd(cmd, []CmdExecute{ipGroups})
		},
	}
	flowAclsCmd := &cobra.Command{
		Use:   "flow-acls",
		Short: "get flow-acls from deepflow-server",
		Run: func(cmd *cobra.Command, args []string) {
			initCmd(cmd, []CmdExecute{flowAcls})
		},
	}
	tapTypesCmd := &cobra.Command{
		Use:   "tap-types",
		Short: "get tap-types from deepflow-server",
		Run: func(cmd *cobra.Command, args []string) {
			initCmd(cmd, []CmdExecute{tapTypes})
		},
	}
	segmentsCmd := &cobra.Command{
		Use:   "segments",
		Short: "get segments from deepflow-server",
		Run: func(cmd *cobra.Command, args []string) {
			initCmd(cmd, []CmdExecute{segments})
		},
	}
	containersCmd := &cobra.Command{
		Use:   "containers",
		Short: "get containers from deepflow-server",
		Run: func(cmd *cobra.Command, args []string) {
			initCmd(cmd, []CmdExecute{containers})
		},
	}
	vpcIPCmd := &cobra.Command{
		Use:   "vpc-ip",
		Short: "get vpc-ip from deepflow-server",
		Run: func(cmd *cobra.Command, args []string) {
			initCmd(cmd, []CmdExecute{vpcIP})
		},
	}
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "get config from deepflow-server",
		Run: func(cmd *cobra.Command, args []string) {
			initCmd(cmd, []CmdExecute{configData})
		},
	}
	skipInterfaceCmd := &cobra.Command{
		Use:   "skip-interface",
		Short: "get skip-interface from deepflow-server",
		Run: func(cmd *cobra.Command, args []string) {
			initCmd(cmd, []CmdExecute{skipInterface})
		},
	}
	localServersCmd := &cobra.Command{
		Use:   "local-servers",
		Short: "get local-servers from deepflow-server",
		Run: func(cmd *cobra.Command, args []string) {
			initCmd(cmd, []CmdExecute{localServers})
		},
	}
	gpidAgentResponseCmd := &cobra.Command{
		Use:   "gpid-agent-response",
		Short: "get gpid-agent-response from deepflow-server",
		Run: func(cmd *cobra.Command, args []string) {
			gpidAgentResponse(cmd)
		},
	}
	gpidGlobalTableCmd := &cobra.Command{
		Use:   "gpid-global-table",
		Short: "get gpid-global-table from deepflow-server",
		Run: func(cmd *cobra.Command, args []string) {
			gpidGlobalTable(cmd)
		},
	}

	gpidAgentRequestCmd := &cobra.Command{
		Use:   "gpid-agent-request",
		Short: "get gpid-agent-request from deepflow-server",
		Run: func(cmd *cobra.Command, args []string) {
			gpidAgentRequest(cmd)
		},
	}
	realGlobalCmd := &cobra.Command{
		Use:   "realclient-to-realserver",
		Short: "get realclient-to-realserver from deepflow-server",
		Run: func(cmd *cobra.Command, args []string) {
			realGlobal(cmd)
		},
	}

	ripToVipCmd := &cobra.Command{
		Use:   "rip-to-vip",
		Short: "get rip-to-vip from deepflow-server",
		Run: func(cmd *cobra.Command, args []string) {
			ripToVip(cmd)
		},
	}

	pluginCmd := &cobra.Command{
		Use:   "plugin",
		Short: "get plugin from deepflow-server",
		Run: func(cmd *cobra.Command, args []string) {
			plugin(cmd)
		},
	}

	agentCacheCmd := &cobra.Command{
		Use:   "agent-cache",
		Short: "get agent-cache from deepflow-server",
		Run: func(cmd *cobra.Command, args []string) {
			agentCache(cmd)
		},
	}

	allCmd := &cobra.Command{
		Use:   "all",
		Short: "get all data from deepflow-server",
		Run: func(cmd *cobra.Command, args []string) {
			initCmd(cmd, []CmdExecute{platformData, ipGroups, flowAcls, tapTypes,
				segments, containers, vpcIP, configData, skipInterface, localServers})
		},
	}

	universalTagNameCmd := &cobra.Command{
		Use:   "universal-tag-name",
		Short: "get universal tag name map from deepflow-server",
		Run: func(cmd *cobra.Command, args []string) {
			universalTagName(cmd)
		},
	}

	orgIDsCmd := &cobra.Command{
		Use:   "orgIDs",
		Short: "get orgIDs from deepflow-server",
		Run: func(cmd *cobra.Command, args []string) {
			orgIDs(cmd)
		},
	}

	commands := []*cobra.Command{platformDataCmd, ipGroupsCmd, flowAclsCmd,
		tapTypesCmd, configCmd, segmentsCmd, containersCmd, vpcIPCmd, skipInterfaceCmd,
		localServersCmd, gpidAgentResponseCmd, gpidGlobalTableCmd, gpidAgentRequestCmd,
		realGlobalCmd, ripToVipCmd, pluginCmd, agentCacheCmd, allCmd, universalTagNameCmd, orgIDsCmd}
	return commands
}

func RegisterTrisolarisCommand() *cobra.Command {
	trisolarisCmd := &cobra.Command{
		Use:   "trisolaris.check",
		Short: "pull grpc data from deepflow-server",
	}
	trisolarisCmd.PersistentFlags().StringVarP(&paramData.CtrlIP, "cip", "", "", "agent ctrl ip")
	trisolarisCmd.PersistentFlags().StringVarP(&paramData.CtrlMac, "cmac", "", "", "agent ctrl mac")
	trisolarisCmd.PersistentFlags().StringVarP(&paramData.GroupID, "gid", "", "", "agent group ID")
	trisolarisCmd.PersistentFlags().StringVarP(&paramData.TeamID, "tid", "", "", "agent team ID")
	trisolarisCmd.PersistentFlags().StringVarP(&paramData.ClusterID, "cid", "", "", "agent k8s cluster ID")
	trisolarisCmd.PersistentFlags().StringVarP(&paramData.Type, "type", "", "trident", "request type trdient/analyzer")
	trisolarisCmd.PersistentFlags().StringVarP(&paramData.PluginType, "ptype", "", "wasm", "request plugin type")
	trisolarisCmd.PersistentFlags().StringVarP(&paramData.PluginName, "pname", "", "", "request plugin name")
	cmds := regiterCommand()
	for _, handler := range cmds {
		trisolarisCmd.AddCommand(handler)
	}

	return trisolarisCmd
}

func getConn(cmd *cobra.Command) *grpc.ClientConn {
	server := common.GetServerInfo(cmd)
	orgID := common.GetORGID(cmd)
	paramData.RpcIP = server.IP
	paramData.RpcPort = strconv.Itoa(int(server.RpcPort))
	paramData.OrgID = uint32(orgID)
	addr := net.JoinHostPort(paramData.RpcIP, paramData.RpcPort)
	conn, err := grpc.Dial(addr, grpc.WithInsecure(), grpc.WithMaxMsgSize(1024*1024*200))
	if err != nil {
		fmt.Println(err)
		return nil
	}

	return conn
}

func initCmd(cmd *cobra.Command, cmds []CmdExecute) {
	conn := getConn(cmd)
	if conn == nil {
		return
	}
	defer conn.Close()
	var name, groupID, clusterID, teamID string
	var orgID uint32
	switch paramData.Type {
	case "trident":
		name = paramData.Type
		groupID = paramData.GroupID
		clusterID = paramData.ClusterID
		teamID = paramData.TeamID
	case "analyzer":
		name = paramData.Type
		orgID = paramData.OrgID
	default:
		fmt.Printf("type(%s) muste be in [trident, analyzer]", paramData.Type)
		return
	}
	fmt.Printf("request trisolaris(%s), params(%+v)\n", conn.Target(), paramData)
	c := trident.NewSynchronizerClient(conn)
	reqData := &trident.SyncRequest{
		CtrlIp:              &paramData.CtrlIP,
		CtrlMac:             &paramData.CtrlMac,
		VtapGroupIdRequest:  &groupID,
		KubernetesClusterId: &clusterID,
		ProcessName:         &name,
		TeamId:              &teamID,
		OrgId:               &orgID,
	}
	var response *trident.SyncResponse
	var err error
	if paramData.Type == "trident" {
		response, err = c.Sync(context.Background(), reqData)
	} else {
		response, err = c.AnalyzerSync(context.Background(), reqData)
	}
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, cmd := range cmds {
		cmd(response)
	}
}

func gpidAgentResponse(cmd *cobra.Command) {
	conn := getConn(cmd)
	if conn == nil {
		return
	}
	defer conn.Close()
	fmt.Printf("request trisolaris(%s), params(%+v)\n", conn.Target(), paramData)
	c := trident.NewSynchronizerClient(conn)
	reqData := &trident.GPIDSyncRequest{
		CtrlIp:  &paramData.CtrlIP,
		CtrlMac: &paramData.CtrlMac,
		TeamId:  &paramData.TeamID,
	}
	response, err := c.GPIDSync(context.Background(), reqData)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("gpid:")
	for index, entry := range response.Entries {
		JsonFormat(index+1, formatEntries(entry))
	}
}

func formatEntries(entry *trident.GPIDSyncEntry) string {
	buffer := bytes.Buffer{}
	format := "{protocol: %d, epc_id_1: %d, ipv4_1: %s, port_1: %d, pid_1: %d, " +
		"epc_id_0: %d, ipv4_0: %s, port_0: %d, pid_0: %d, epc_id_real: %d, " +
		"ipv4_real: %s, port_real: %d, pid_real: %d, role_real: %d, netns_idx: %d}"
	buffer.WriteString(fmt.Sprintf(format,
		entry.GetProtocol(), entry.GetEpcId_1(), utils.IpFromUint32(entry.GetIpv4_1()).String(), entry.GetPort_1(), entry.GetPid_1(),
		entry.GetEpcId_0(), utils.IpFromUint32(entry.GetIpv4_0()).String(), entry.GetPort_0(), entry.GetPid_0(), entry.GetEpcIdReal(),
		utils.IpFromUint32(entry.GetIpv4Real()).String(), entry.GetPortReal(), entry.GetPidReal(), entry.GetRoleReal(), entry.GetNetnsIdx()),
	)
	return buffer.String()
}

func formatGlobalEntry(entry *trident.GlobalGPIDEntry) string {
	buffer := bytes.Buffer{}
	format := "{ protocol: %d, agent_id_1: %d, epc_id_1: %d, ipv4_1: %s, port_1: %d, pid_1: %d, gpid_1: %d " +
		"agent_id_0: %d, epc_id_0: %d, ipv4_0: %s, port_0: %d, pid_0: %d, gpid_0: %d, netns_idx: %d}"
	buffer.WriteString(fmt.Sprintf(format,
		entry.GetProtocol(),
		entry.GetAgentId_1(), entry.GetEpcId_1(), utils.IpFromUint32(entry.GetIpv4_1()).String(), entry.GetPort_1(), entry.GetPid_1(), entry.GetGpid_1(),
		entry.GetAgentId_0(), entry.GetEpcId_0(), utils.IpFromUint32(entry.GetIpv4_0()).String(), entry.GetPort_0(), entry.GetPid_0(), entry.GetGpid_0(),
		entry.GetNetnsIdx()))

	return buffer.String()
}

func gpidGlobalTable(cmd *cobra.Command) {
	conn := getConn(cmd)
	if conn == nil {
		return
	}
	defer conn.Close()
	fmt.Printf("request trisolaris(%s), params(%+v)\n", conn.Target(), paramData)
	c := trident.NewDebugClient(conn)
	reqData := &trident.GPIDSyncRequest{
		TeamId: &paramData.TeamID,
	}
	response, err := c.DebugGPIDGlobalData(context.Background(), reqData)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("GPIDGlobalData:")
	for index, entry := range response.GetEntries() {
		JsonFormat(index+1, formatGlobalEntry(entry))
	}
}

func gpidAgentRequest(cmd *cobra.Command) {
	conn := getConn(cmd)
	if conn == nil {
		return
	}
	defer conn.Close()
	fmt.Printf("request trisolaris(%s), params(%+v)\n", conn.Target(), paramData)
	c := trident.NewDebugClient(conn)
	reqData := &trident.GPIDSyncRequest{
		CtrlIp:  &paramData.CtrlIP,
		CtrlMac: &paramData.CtrlMac,
		TeamId:  &paramData.TeamID,
	}
	response, err := c.DebugGPIDVTapData(context.Background(), reqData)
	if err != nil {
		fmt.Println(err)
		return
	}
	req := response.GetSyncRequest()
	tm := time.Unix(int64(response.GetUpdateTime()), 0)
	fmt.Printf("response(ctrl_ip: %s ctrl_mac: %s agent_id: %d update_time: %s)\n", req.GetCtrlIp(), req.GetCtrlMac(), req.GetVtapId(), tm.Format("2006-01-02 15:04:05"))
	fmt.Println("Entries:")
	if req == nil {
		return
	}
	for index, entry := range req.GetEntries() {
		JsonFormat(index+1, formatEntries(entry))
	}
}

func formatRealEntry(entry *trident.RealClientToRealServer) string {
	buffer := bytes.Buffer{}
	format := "{epc_id_1: %d, ipv4_1: %s, port_1: %d, " +
		"epc_id_0: %d, ipv4_0: %s, port_0: %d, " +
		"epc_id_real: %d, ipv4_real: %s, port_real: %d, pid_real: %d, agent_id_real: %d}"
	buffer.WriteString(fmt.Sprintf(format,
		entry.GetEpcId_1(), utils.IpFromUint32(entry.GetIpv4_1()).String(), entry.GetPort_1(),
		entry.GetEpcId_0(), utils.IpFromUint32(entry.GetIpv4_0()).String(), entry.GetPort_0(),
		entry.GetEpcIdReal(), utils.IpFromUint32(entry.GetIpv4Real()).String(),
		entry.GetPortReal(), entry.GetPidReal(), entry.GetAgentIdReal()))
	return buffer.String()
}

func realGlobal(cmd *cobra.Command) {
	conn := getConn(cmd)
	if conn == nil {
		return
	}
	defer conn.Close()
	fmt.Printf("request trisolaris(%s), params(%+v)\n", conn.Target(), paramData)
	c := trident.NewDebugClient(conn)
	reqData := &trident.GPIDSyncRequest{
		CtrlIp:  &paramData.CtrlIP,
		CtrlMac: &paramData.CtrlMac,
		TeamId:  &paramData.TeamID,
	}
	response, err := c.DebugRealGlobalData(context.Background(), reqData)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("Entries:")
	for index, entry := range response.GetEntries() {
		JsonFormat(index+1, formatRealEntry(entry))
	}
}

func formatRVEntry(entry *trident.RipToVip) string {
	buffer := bytes.Buffer{}
	format := "{protocol: %d, epc_id: %d, r_ipv4: %s, r_port: %d, " +
		" v_ipv4: %s, v_port: %d, }"
	buffer.WriteString(fmt.Sprintf(format,
		entry.GetProtocol(), entry.GetEpcId(),
		utils.IpFromUint32(entry.GetRIpv4()).String(), entry.GetRPort(),
		utils.IpFromUint32(entry.GetVIpv4()).String(), entry.GetVPort(),
	))
	return buffer.String()
}

func ripToVip(cmd *cobra.Command) {
	conn := getConn(cmd)
	if conn == nil {
		return
	}
	defer conn.Close()
	fmt.Printf("request trisolaris(%s), params(%+v)\n", conn.Target(), paramData)
	c := trident.NewDebugClient(conn)
	reqData := &trident.GPIDSyncRequest{
		CtrlIp:  &paramData.CtrlIP,
		CtrlMac: &paramData.CtrlMac,
		TeamId:  &paramData.TeamID,
	}
	response, err := c.DebugRIPToVIP(context.Background(), reqData)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("Entries:")
	for index, entry := range response.GetEntries() {
		JsonFormat(index+1, formatRVEntry(entry))
	}
}

func agentCache(cmd *cobra.Command) {
	conn := getConn(cmd)
	if conn == nil {
		return
	}
	defer conn.Close()
	fmt.Printf("request trisolaris(%s), params(%+v)\n", conn.Target(), paramData)
	c := trident.NewDebugClient(conn)
	reqData := &trident.AgentCacheRequest{
		CtrlIp:  &paramData.CtrlIP,
		CtrlMac: &paramData.CtrlMac,
		TeamId:  &paramData.TeamID,
	}
	response, err := c.DebugAgentCache(context.Background(), reqData)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("%s\n", response.GetContent())
}

func JsonFormat(index int, v interface{}) {
	jsonBytes, err := json.Marshal(v)
	if err != nil {
		fmt.Println("json encode failed")
	}
	fmt.Printf("\t%v: %s\n", index, jsonBytes)
}

func (a SortedAcls) Len() int {
	return len(a)
}

func (a SortedAcls) Less(i, j int) bool {
	return a[i].GetId() < a[j].GetId()
}

func (a SortedAcls) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}

func flowAcls(response *trident.SyncResponse) {
	flowAcls := trident.FlowAcls{}
	fmt.Println("flow Acls version:", response.GetVersionAcls())

	if flowAclsCompressed := response.GetFlowAcls(); flowAclsCompressed != nil {
		if err := flowAcls.Unmarshal(flowAclsCompressed); err == nil {
			sort.Sort(SortedAcls(flowAcls.FlowAcl)) // sort by id
			fmt.Println("flow Acls:")
			for index, entry := range flowAcls.FlowAcl {
				JsonFormat(index+1, entry)
			}
		}
	}
}

func ipGroups(response *trident.SyncResponse) {
	groups := trident.Groups{}
	fmt.Println("Groups version:", response.GetVersionGroups())

	if groupsCompressed := response.GetGroups(); groupsCompressed != nil {
		if err := groups.Unmarshal(groupsCompressed); err == nil {
			fmt.Println("Groups data:")
			for index, entry := range groups.Groups {
				JsonFormat(index+1, entry)
			}
			fmt.Println("Services data:")
			for index, entry := range groups.Svcs {
				JsonFormat(index+1, entry)
			}
		}
	}
}

func Uint64ToMac(v uint64) net.HardwareAddr {
	bytes := [8]byte{}
	BigEndian.PutUint64(bytes[:], v)
	return net.HardwareAddr(bytes[2:])
}

func formatString(data *trident.Interface) string {
	buffer := bytes.Buffer{}
	format := "Id: %d Mac: %s EpcId: %d DeviceType: %d DeviceId: %d IfType: %d" +
		" LaunchServer: %s LaunchServerId: %d RegionId: %d AzId: %d, PodGroupId: %d, " +
		"PodNsId: %d, PodId: %d, PodClusterId: %d, PodGroupType: %d, NetnsId: %d, AgentId: %d, IsVipInterface: %t "
	buffer.WriteString(fmt.Sprintf(format, data.GetId(), Uint64ToMac(data.GetMac()),
		data.GetEpcId(), data.GetDeviceType(), data.GetDeviceId(), data.GetIfType(),
		data.GetLaunchServer(), data.GetLaunchServerId(), data.GetRegionId(),
		data.GetAzId(), data.GetPodGroupId(), data.GetPodNsId(), data.GetPodId(),
		data.GetPodClusterId(), data.GetPodGroupType(), data.GetNetnsId(),
		data.GetVtapId(), data.GetIsVipInterface()))
	if data.GetPodNodeId() > 0 {
		buffer.WriteString(fmt.Sprintf("PodNodeId: %d ", data.GetPodNodeId()))
	}
	if len(data.GetIpResources()) > 0 {
		buffer.WriteString(fmt.Sprintf("IpResources: %s", data.GetIpResources()))
	}
	return buffer.String()
}

func platformData(response *trident.SyncResponse) {
	platform := trident.PlatformData{}
	fmt.Println("PlatformData version:", response.GetVersionPlatformData())

	if plarformCompressed := response.GetPlatformData(); plarformCompressed != nil {
		if err := platform.Unmarshal(plarformCompressed); err == nil {
			fmt.Println("interfaces:")
			for index, entry := range platform.Interfaces {
				JsonFormat(index+1, formatString(entry))
			}
			fmt.Println("peer connections:")
			for index, entry := range platform.PeerConnections {
				JsonFormat(index+1, entry)
			}
			fmt.Println("cidrs:")
			for index, entry := range platform.Cidrs {
				JsonFormat(index+1, entry)
			}
			fmt.Println("gprocess infos:")
			for index, entry := range platform.GprocessInfos {
				JsonFormat(index+1, entry)
			}
		}
	}
}

func configData(response *trident.SyncResponse) {
	fmt.Println("config:")
	config := response.GetConfig()
	fmt.Println(proto.MarshalTextString(config))
	fmt.Println("revision:", response.GetRevision())
	fmt.Println("self_update_url:", response.GetSelfUpdateUrl())

	fmt.Println("\nAnalyzerconfig:")
	fmt.Println(proto.MarshalTextString(response.GetAnalyzerConfig()))
}

func skipInterface(response *trident.SyncResponse) {
	fmt.Println("SkipInterface:")
	for index, skipInterface := range response.GetSkipInterface() {
		JsonFormat(index+1, fmt.Sprintf("mac: %s", Uint64ToMac(skipInterface.GetMac())))
	}
}

func localServers(response *trident.SyncResponse) {
	fmt.Println("localServers:")
	for index, entry := range response.GetDeepflowServerInstances() {
		JsonFormat(index+1, entry)
	}
}

func tapTypes(response *trident.SyncResponse) {
	fmt.Println("taptypes:")
	tapTypes := response.GetTapTypes()
	for index, tapType := range tapTypes {
		JsonFormat(index+1, tapType)
	}
}

func segments(response *trident.SyncResponse) {
	fmt.Println("local_segments:")
	localSegments := response.GetLocalSegments()
	for index, localSegment := range localSegments {
		JsonFormat(index+1, localSegment)
	}
	fmt.Println("remote_segments:")
	remoteSegments := response.GetRemoteSegments()
	for index, remoteSegment := range remoteSegments {
		JsonFormat(index+1, remoteSegment)
	}
}

func containers(response *trident.SyncResponse) {
	fmt.Println("containers:")
	containers := response.GetContainers()
	for index, container := range containers {
		JsonFormat(index+1, container)
	}
}

func vpcIP(response *trident.SyncResponse) {
	fmt.Println("agent_ip:")
	for index, vtapIP := range response.GetVtapIps() {
		JsonFormat(index+1, vtapIP)
	}
	fmt.Println("pod_ip:")
	for index, podIP := range response.GetPodIps() {
		JsonFormat(index+1, podIP)
	}
}

func plugin(cmd *cobra.Command) {
	conn := getConn(cmd)
	if conn == nil {
		return
	}
	defer conn.Close()
	fmt.Printf("request trisolaris(%s), params(%+v)\n", conn.Target(), paramData)
	var pluginType trident.PluginType
	switch paramData.PluginType {
	case "wasm":
		pluginType = trident.PluginType_WASM
	default:
		fmt.Printf("request pluginType(%s) not supported, pluginType must be in %s\n",
			paramData.PluginType, []string{"wasm"})
		return
	}
	c := trident.NewSynchronizerClient(conn)
	reqData := &trident.PluginRequest{
		CtrlIp:     &paramData.CtrlIP,
		CtrlMac:    &paramData.CtrlMac,
		TeamId:     &paramData.TeamID,
		PluginType: &pluginType,
		PluginName: &paramData.PluginName,
	}
	stream, err := c.Plugin(context.Background(), reqData)
	if err != nil {
		fmt.Println(err)
		return
	}
	var (
		data []byte
		md5  string
	)
	for {
		if res, err := stream.Recv(); err == nil {
			data = append(data, res.GetContent()...)
			md5 = res.GetMd5()
		} else {
			if errors.Is(err, io.EOF) {
				break
			}
			fmt.Println(res, err)
			return
		}
	}
	fileName := paramData.PluginType + "-" + paramData.PluginName
	err = ioutil.WriteFile(fileName, data, 0666)
	if err != nil {
		fmt.Printf("save plugin(%s) fail %s\n", fileName, err)
		return
	}
	fmt.Printf("save plugin(%s) success, md5=%s\n", fileName, md5)
}

func universalTagName(cmd *cobra.Command) {
	conn := getConn(cmd)
	if conn == nil {
		return
	}
	defer conn.Close()
	fmt.Printf("request trisolaris(%s), params(%+v)\n", conn.Target(), paramData)
	client := trident.NewSynchronizerClient(conn)
	resp, err := client.GetUniversalTagNameMaps(context.Background(),
		&trident.UniversalTagNameMapsRequest{OrgId: &paramData.OrgID})
	if err != nil {
		fmt.Println(err)
		return
	}

	b, err := json.MarshalIndent(resp, "", "\t")
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println(string(b))
}

func orgIDs(cmd *cobra.Command) {
	conn := getConn(cmd)
	if conn == nil {
		return
	}
	defer conn.Close()
	fmt.Printf("request trisolaris(%s), params(%+v)\n", conn.Target(), paramData)
	client := trident.NewSynchronizerClient(conn)
	resp, err := client.GetOrgIDs(context.Background(), &trident.OrgIDsRequest{})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(resp)
}
