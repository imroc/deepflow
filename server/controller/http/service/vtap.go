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

package service

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	mapset "github.com/deckarep/golang-set"
	"github.com/google/uuid"

	"github.com/deepflowio/deepflow/server/controller/common"
	"github.com/deepflowio/deepflow/server/controller/config"
	"github.com/deepflowio/deepflow/server/controller/db/mysql"
	httpcommon "github.com/deepflowio/deepflow/server/controller/http/common"
	. "github.com/deepflowio/deepflow/server/controller/http/service/common"
	"github.com/deepflowio/deepflow/server/controller/http/service/rebalance"
	"github.com/deepflowio/deepflow/server/controller/model"
	monitorconf "github.com/deepflowio/deepflow/server/controller/monitor/config"
	"github.com/deepflowio/deepflow/server/controller/monitor/license"
	"github.com/deepflowio/deepflow/server/controller/trisolaris/refresh"
	"github.com/deepflowio/deepflow/server/controller/trisolaris/utils"
)

type Agent struct {
	cfg *config.ControllerConfig

	resourceAccess *ResourceAccess
}

func NewAgent(userInfo *httpcommon.UserInfo, cfg *config.ControllerConfig) *Agent {
	return &Agent{
		cfg:            cfg,
		resourceAccess: &ResourceAccess{fpermit: cfg.FPermit, userInfo: userInfo},
	}
}

const (
	VTAP_LICENSE_CHECK_EXCEPTION = "采集器(%s)不支持修改为指定授权类型"
)

func (a *Agent) Get(filter map[string]interface{}) (resp []model.Vtap, err error) {
	var response []model.Vtap
	var allVTaps []mysql.VTap
	var vtapGroups []mysql.VTapGroup
	var regions []mysql.Region
	var azs []mysql.AZ
	var vtapRepos []mysql.VTapRepo

	userInfo := a.resourceAccess.userInfo
	dbInfo, err := mysql.GetDB(userInfo.ORGID)
	if err != nil {
		return nil, err
	}
	Db, vtapDB := dbInfo.DB, dbInfo.DB
	for _, param := range []string{
		"lcuuid", "name", "type", "vtap_group_lcuuid", "controller_ip", "analyzer_ip", "team_id",
	} {
		where := fmt.Sprintf("%s = ?", param)
		if _, ok := filter[param]; ok {
			vtapDB = vtapDB.Where(where, filter[param])
		}
	}
	if _, ok := filter["names"]; ok {
		if len(filter["names"].([]string)) > 0 {
			vtapDB = vtapDB.Where("name IN (?)", filter["names"].([]string))
		}
	}

	if err := vtapDB.Find(&allVTaps).Error; err != nil {
		return nil, err
	}
	if err := Db.Find(&vtapGroups).Error; err != nil {
		return nil, err
	}
	if err := Db.Find(&regions).Error; err != nil {
		return nil, err
	}
	if err := Db.Find(&azs).Error; err != nil {
		return nil, err
	}
	if err := Db.Select("name", "branch", "rev_count").Find(&vtapRepos).Error; err != nil {
		return nil, err
	}

	lcuuidToRegion := make(map[string]string)
	for _, region := range regions {
		lcuuidToRegion[region.Lcuuid] = region.Name
	}

	lcuuidToAz := make(map[string]string)
	azToRegion := make(map[string]string)
	for _, az := range azs {
		lcuuidToAz[az.Lcuuid] = az.Name
		azToRegion[az.Lcuuid] = az.Region
	}

	lcuuidToGroup := make(map[string]string)
	for _, group := range vtapGroups {
		lcuuidToGroup[group.Lcuuid] = group.Name
	}

	vtapRepoNameToRevision := make(map[string]string, len(vtapRepos))
	for _, item := range vtapRepos {
		vtapRepoNameToRevision[item.Name] = item.Branch + " " + item.RevCount
	}

	agents, err := getAgentByUser(userInfo, &a.cfg.FPermit, allVTaps)
	if err != nil {
		return nil, err
	}
	for _, vtap := range agents {
		vtapResp := model.Vtap{
			ID:               vtap.ID,
			Name:             vtap.Name,
			Lcuuid:           vtap.Lcuuid,
			Enable:           vtap.Enable,
			Type:             vtap.Type,
			CtrlIP:           vtap.CtrlIP,
			CtrlMac:          vtap.CtrlMac,
			ControllerIP:     vtap.ControllerIP,
			AnalyzerIP:       vtap.AnalyzerIP,
			CurControllerIP:  vtap.CurControllerIP,
			CurAnalyzerIP:    vtap.CurAnalyzerIP,
			BootTime:         vtap.BootTime,
			CPUNum:           vtap.CPUNum,
			MemorySize:       vtap.MemorySize,
			Arch:             vtap.Arch,
			ArchType:         common.GetArchType(vtap.Arch),
			Os:               vtap.Os,
			OsType:           common.GetOsType(vtap.Os),
			KernelVersion:    vtap.KernelVersion,
			ProcessName:      vtap.ProcessName,
			CurrentK8sImage:  vtap.CurrentK8sImage,
			LicenseType:      vtap.LicenseType,
			ExpectedRevision: vtap.ExpectedRevision,
			UpgradePackage:   vtap.UpgradePackage,
			TapMode:          vtap.TapMode,
			TeamID:           vtap.TeamID,
		}
		// state
		if vtap.Enable == common.VTAP_ENABLE_FALSE {
			vtapResp.State = common.VTAP_STATE_DISABLE
		} else {
			vtapResp.State = vtap.State
		}
		// revision
		revision := ""
		completeRevision := vtap.Revision
		revisionSplit := strings.Split(vtap.Revision, "-")
		if len(revisionSplit) >= 2 {
			revision = revisionSplit[0]
			completeRevision = revisionSplit[1]
		}
		vtapResp.Revision = revision
		vtapResp.CompleteRevision = completeRevision
		if vtap.UpgradePackage != "" {
			if upgradeRevision, ok := vtapRepoNameToRevision[vtap.UpgradePackage]; ok {
				vtapResp.UpgradeRevision = upgradeRevision
			} else {
				log.Errorf("ORG(id=%d database=%s) vtap upgrade package(%v) cannot assoicated with vtap repo",
					dbInfo.ORGID, dbInfo.Name, vtap.UpgradePackage)
			}
		}
		// exceptions
		exceptions := vtap.Exceptions
		bitNum := 0
		for ; exceptions > 0; exceptions /= 2 {
			if exceptions%2 != 0 {
				vtapResp.Exceptions = append(vtapResp.Exceptions, 1<<bitNum)
			}
			bitNum += 1
		}
		// license_functions
		functions := strings.Split(vtap.LicenseFunctions, ",")
		for _, function := range functions {
			functionInt, err := strconv.Atoi(function)
			if err != nil {
				continue
			}
			vtapResp.LicenseFunctions = append(vtapResp.LicenseFunctions, functionInt)
		}
		// az
		vtapResp.AZ = vtap.AZ
		if azName, ok := lcuuidToAz[vtap.AZ]; ok {
			vtapResp.AZName = azName
		}
		// vtap_group
		vtapResp.VtapGroupLcuuid = vtap.VtapGroupLcuuid
		if groupName, ok := lcuuidToGroup[vtap.VtapGroupLcuuid]; ok {
			vtapResp.VtapGroupName = groupName
		}
		// regions
		vtapResp.Region = vtap.Region
		if len(vtapResp.Region) == 0 {
			if region, ok := azToRegion[vtap.AZ]; ok {
				vtapResp.Region = region
			}
		}
		if regionName, ok := lcuuidToRegion[vtapResp.Region]; ok {
			vtapResp.RegionName = regionName
		}

		switch vtap.Type {
		case common.VTAP_TYPE_KVM, common.VTAP_TYPE_ESXI, common.VTAP_TYPE_WORKLOAD_V,
			common.VTAP_TYPE_POD_HOST, common.VTAP_TYPE_POD_VM, common.VTAP_TYPE_HYPER_V,
			common.VTAP_TYPE_WORKLOAD_P, common.VTAP_TYPE_K8S_SIDECAR:
			vtapResp.LaunchServer = vtap.LaunchServer
			vtapResp.LaunchServerID = vtap.LaunchServerID
			vtapResp.SyncedControllerAt = vtap.SyncedControllerAt.Format(common.GO_BIRTHDAY)
			vtapResp.SyncedAnalyzerAt = vtap.SyncedAnalyzerAt.Format(common.GO_BIRTHDAY)
		default:
			if vtap.CreatedAt.Before(vtap.SyncedControllerAt) {
				vtapResp.SyncedControllerAt = vtap.SyncedControllerAt.Format(common.GO_BIRTHDAY)
			}
			if vtap.CreatedAt.Before(vtap.SyncedAnalyzerAt) {
				vtapResp.SyncedAnalyzerAt = vtap.SyncedAnalyzerAt.Format(common.GO_BIRTHDAY)
			}
		}

		response = append(response, vtapResp)
	}
	return response, nil
}

func (a *Agent) Create(vtapCreate model.VtapCreate) (model.Vtap, error) {
	if err := a.resourceAccess.CanAddResource(vtapCreate.TeamID, common.SET_RESOURCE_TYPE_AGENT, ""); err != nil {
		return model.Vtap{}, err
	}
	dbInfo, err := mysql.GetDB(a.resourceAccess.userInfo.ORGID)
	if err != nil {
		return model.Vtap{}, err
	}
	db := dbInfo.DB

	var vtap mysql.VTap
	if ret := db.Where("ctrl_ip = ?", vtapCreate.CtrlIP).First(&vtap); ret.Error == nil {
		return model.Vtap{}, NewError(
			httpcommon.RESOURCE_ALREADY_EXIST,
			fmt.Sprintf("vtap (ctrl_ip: %s) already exist", vtapCreate.CtrlIP),
		)
	}

	if ret := db.Where("name = ?", vtapCreate.Name).First(&vtap); ret.Error == nil {
		return model.Vtap{}, NewError(
			httpcommon.RESOURCE_ALREADY_EXIST,
			fmt.Sprintf("vtap (%s) already exist", vtapCreate.Name),
		)
	}

	// vtap name not support space && :
	vtapName := vtapCreate.Name
	strings.Replace(vtapName, ":", "-", -1)
	strings.Replace(vtapName, " ", "-", -1)

	vtap = mysql.VTap{}
	lcuuid := uuid.New().String()
	vtap.Lcuuid = lcuuid
	vtap.Name = vtapName
	vtap.Type = vtapCreate.Type
	vtap.Enable = common.VTAP_ENABLE_TRUE
	vtap.CtrlIP = vtapCreate.CtrlIP
	vtap.CtrlMac = vtapCreate.CtrlMac
	vtap.LaunchServer = vtapCreate.CtrlIP
	vtap.AZ = vtapCreate.AZ
	vtap.Region = vtapCreate.Region
	vtap.VtapGroupLcuuid = vtapCreate.VtapGroupLcuuid
	vtap.TeamID = vtapCreate.TeamID
	switch vtapCreate.Type {
	case common.VTAP_TYPE_DEDICATED:
		vtap.TapMode = common.TAPMODE_ANALYZER
	case common.VTAP_TYPE_TUNNEL_DECAPSULATION:
		vtap.TapMode = common.TAPMODE_DECAP
	}
	db.Create(&vtap)

	response, _ := a.Get(map[string]interface{}{"lcuuid": lcuuid})
	return response[0], err
}

func (a *Agent) Update(lcuuid, name string, vtapUpdate map[string]interface{}) (resp model.Vtap, err error) {
	orgID := a.resourceAccess.userInfo.ORGID
	dbInfo, err := mysql.GetDB(orgID)
	if err != nil {
		return model.Vtap{}, err
	}
	db := dbInfo.DB

	var vtap mysql.VTap
	var dbUpdateMap = make(map[string]interface{})

	if lcuuid != "" {
		if ret := db.Where("lcuuid = ?", lcuuid).First(&vtap); ret.Error != nil {
			return model.Vtap{}, NewError(httpcommon.RESOURCE_NOT_FOUND, fmt.Sprintf("vtap (%s) not found", lcuuid))
		}
	} else if name != "" {
		if ret := db.Where("name = ?", name).First(&vtap); ret.Error != nil {
			return model.Vtap{}, NewError(httpcommon.RESOURCE_NOT_FOUND, fmt.Sprintf("vtap (%s) not found", name))
		}
	} else {
		return model.Vtap{}, NewError(httpcommon.INVALID_PARAMETERS, "must specify name or lcuuid")
	}
	if err := a.resourceAccess.CanUpdateResource(vtap.TeamID, common.SET_RESOURCE_TYPE_AGENT, "", nil); err != nil {
		return model.Vtap{}, err
	}

	log.Infof("ORG(id=%d database=%s) update vtap (%s) config %v", dbInfo.ORGID, dbInfo.Name, vtap.Name, vtapUpdate)

	// enable/state/vtap_group_lcuuid
	for _, key := range []string{"ENABLE", "STATE", "VTAP_GROUP_LCUUID", "LICENSE_TYPE"} {
		if _, ok := vtapUpdate[key]; ok {
			dbUpdateMap[strings.ToLower(key)] = vtapUpdate[key]
		}
	}

	if licenseFunctions, ok := vtapUpdate["LICENSE_FUNCTIONS"].([]interface{}); ok {
		licenseFunctionStrs := []string{}
		for _, licenseFunction := range licenseFunctions {
			licenseFunctionStrs = append(licenseFunctionStrs, strconv.Itoa(int(licenseFunction.(float64))))
		}
		dbUpdateMap["license_functions"] = strings.Join(licenseFunctionStrs, ",")
	}

	db.Model(&vtap).Updates(dbUpdateMap)

	if value, ok := vtapUpdate["ENABLE"]; ok && value == float64(0) {
		key := vtap.CtrlIP + "-" + vtap.CtrlMac
		if err := db.Delete(&mysql.KubernetesCluster{}, "value = ?", key).Error; err != nil {
			log.Errorf("ORG(id=%d database=%s) error: %v", dbInfo.ORGID, dbInfo.Name, err)
		}
	}

	response, _ := a.Get(map[string]interface{}{"lcuuid": vtap.Lcuuid})
	refresh.RefreshCache(orgID, []common.DataChanged{common.DATA_CHANGED_VTAP})
	return response[0], nil
}

func (a *Agent) BatchUpdate(updateMap []map[string]interface{}) (resp map[string][]string, err error) {
	var description string
	var succeedLcuuids []string
	var failedLcuuids []string

	for _, vtapUpdate := range updateMap {
		if lcuuid, ok := vtapUpdate["LCUUID"].(string); ok {
			_, _err := a.Update(lcuuid, "", vtapUpdate)
			if _err != nil {
				description += _err.Error()
				failedLcuuids = append(failedLcuuids, lcuuid)
			} else {
				succeedLcuuids = append(succeedLcuuids, lcuuid)
			}
		}
	}

	response := map[string][]string{
		"SUCCEED_LCUUID": succeedLcuuids,
		"FAILED_LCUUID":  failedLcuuids,
	}

	if description != "" {
		return response, NewError(httpcommon.SERVER_ERROR, description)
	} else {
		return response, nil
	}
}

func (a *Agent) checkLicenseType(vtap mysql.VTap, licenseType int) (err error) {
	// check current vtap if support wanted licenseType
	supportedLicenseTypes := license.GetSupportedLicenseType(vtap.Type)
	if len(supportedLicenseTypes) > 0 {
		sort.Ints(supportedLicenseTypes)
		index := sort.SearchInts(supportedLicenseTypes, licenseType)
		if index >= len(supportedLicenseTypes) || supportedLicenseTypes[index] != licenseType {
			return NewError(httpcommon.INVALID_POST_DATA, fmt.Sprintf(VTAP_LICENSE_CHECK_EXCEPTION, vtap.Name))
		}
	} else {
		return NewError(httpcommon.INVALID_POST_DATA, fmt.Sprintf(VTAP_LICENSE_CHECK_EXCEPTION, vtap.Name))
	}
	return nil
}

func (a *Agent) UpdateVtapLicenseType(lcuuid string, vtapUpdate map[string]interface{}) (resp model.Vtap, err error) {
	dbInfo, err := mysql.GetDB(a.resourceAccess.userInfo.ORGID)
	if err != nil {
		return model.Vtap{}, err
	}
	db := dbInfo.DB

	var vtap mysql.VTap
	var dbUpdateMap = make(map[string]interface{})

	if ret := db.Where("lcuuid = ?", lcuuid).First(&vtap); ret.Error != nil {
		return model.Vtap{}, NewError(httpcommon.RESOURCE_NOT_FOUND, fmt.Sprintf("vtap (%s) not found", lcuuid))
	}
	if err := a.resourceAccess.CanUpdateResource(vtap.TeamID, common.SET_RESOURCE_TYPE_AGENT, "", nil); err != nil {
		return model.Vtap{}, err
	}

	log.Infof("ORG(id=%d database=%s) update vtap (%s) license %v", dbInfo.ORGID, dbInfo.Name, vtap.Name, vtapUpdate)

	if _, ok := vtapUpdate["LICENSE_TYPE"]; ok {
		dbUpdateMap["license_type"] = vtapUpdate["LICENSE_TYPE"]
		licenseType := int(vtapUpdate["LICENSE_TYPE"].(float64))

		// 检查是否可以修改
		err := a.checkLicenseType(vtap, licenseType)
		if err != nil {
			return model.Vtap{}, err
		}
	}

	if licenseFunctions, ok := vtapUpdate["LICENSE_FUNCTIONS"].([]interface{}); ok {
		licenseFunctionStrs := []string{}
		for _, licenseFunction := range licenseFunctions {
			licenseFunctionStrs = append(licenseFunctionStrs, strconv.Itoa(int(licenseFunction.(float64))))
		}
		dbUpdateMap["license_functions"] = strings.Join(licenseFunctionStrs, ",")
	}

	// 更新vtap DB
	db.Model(&vtap).Updates(dbUpdateMap)

	response, _ := a.Get(map[string]interface{}{"lcuuid": vtap.Lcuuid})
	return response[0], nil
}

func (a *Agent) BatchUpdateVtapLicenseType(updateMap []map[string]interface{}) (resp map[string][]string, err error) {
	dbInfo, err := mysql.GetDB(a.resourceAccess.userInfo.ORGID)
	if err != nil {
		return nil, err
	}
	db := dbInfo.DB

	var description string
	var succeedLcuuids []string
	var failedLcuuids []string

	for _, vtapUpdate := range updateMap {
		if lcuuid, ok := vtapUpdate["LCUUID"].(string); ok {
			var _err error
			var vtap mysql.VTap
			var dbUpdateMap = make(map[string]interface{})

			if ret := db.Where("lcuuid = ?", lcuuid).First(&vtap); ret.Error != nil {
				_err = NewError(httpcommon.RESOURCE_NOT_FOUND, fmt.Sprintf("vtap (%s) not found", lcuuid))
			} else {
				// 检查是否可以修改
				licenseType := int(vtapUpdate["LICENSE_TYPE"].(float64))
				_err = a.checkLicenseType(vtap, licenseType)
				if _err == nil {
					// 更新vtap DB
					dbUpdateMap["license_type"] = vtapUpdate["LICENSE_TYPE"]

					if licenseFunctions, ok := vtapUpdate["LICENSE_FUNCTIONS"].([]interface{}); ok {
						licenseFunctionStrs := []string{}
						for _, licenseFunction := range licenseFunctions {
							licenseFunctionStrs = append(
								licenseFunctionStrs,
								strconv.Itoa(int(licenseFunction.(float64))),
							)
						}
						dbUpdateMap["license_functions"] = strings.Join(licenseFunctionStrs, ",")
					}
					db.Model(&vtap).Updates(dbUpdateMap)
				}
			}
			if _err != nil {
				description += _err.Error()
				failedLcuuids = append(failedLcuuids, lcuuid)
			} else {
				succeedLcuuids = append(succeedLcuuids, lcuuid)
			}
		}
	}

	response := map[string][]string{
		"SUCCEED_LCUUID": succeedLcuuids,
		"FAILED_LCUUID":  failedLcuuids,
	}

	if description != "" {
		return response, NewError(httpcommon.SERVER_ERROR, description)
	} else {
		return response, nil
	}
}

func (a *Agent) Delete(lcuuid string) (resp map[string]string, err error) {
	dbInfo, err := mysql.GetDB(a.resourceAccess.userInfo.ORGID)
	if err != nil {
		return nil, err
	}
	db := dbInfo.DB

	var vtap mysql.VTap
	if ret := db.Where("lcuuid = ?", lcuuid).First(&vtap); ret.Error != nil {
		return map[string]string{}, NewError(httpcommon.RESOURCE_NOT_FOUND, fmt.Sprintf("vtap (%s) not found", lcuuid))
	}
	if err := a.resourceAccess.CanDeleteResource(vtap.TeamID, common.SET_RESOURCE_TYPE_AGENT, ""); err != nil {
		return nil, err
	}

	log.Infof("ORG(id=%d database=%s) delete vtap (%s)", dbInfo.ORGID, dbInfo.Name, vtap.Name)

	db.Delete(&vtap)
	return map[string]string{"LCUUID": lcuuid}, nil
}

func (a *Agent) BatchDelete(deleteMap []map[string]string) (resp map[string][]string, err error) {
	var description string
	var deleteLcuuids []string
	var failedLcuuids []string

	for _, vtapDelete := range deleteMap {
		if lcuuid, ok := vtapDelete["LCUUID"]; ok {
			_, _err := a.Delete(lcuuid)
			if _err != nil {
				description += _err.Error()
				failedLcuuids = append(failedLcuuids, lcuuid)
			} else {
				deleteLcuuids = append(deleteLcuuids, lcuuid)
			}
		}
	}

	response := map[string][]string{
		"DELETE_LCUUID": deleteLcuuids,
		"FAILED_LCUUID": failedLcuuids,
	}

	if description != "" {
		return response, NewError(httpcommon.SERVER_ERROR, description)
	} else {
		return response, nil
	}
}

func execAZRebalance(
	db *mysql.DB, azLcuuid string, vtapNum int, hostType string, hostIPToVTaps map[string][]*mysql.VTap,
	hostIPToAvailableVTapNum map[string]int, hostIPToUsedVTapNum map[string]int,
	hostIPToState map[string]int, ifCheck bool,
) model.AZVTapRebalanceResult {

	// 生成可分配的控制器/数据节点列表
	availableHostNum := 0
	hostAvailableVTapNum := []common.KVPair{}
	hostIPToRebalanceResult := make(map[string]*model.HostVTapRebalanceResult)
	for hostIP, availableVTapNum := range hostIPToAvailableVTapNum {
		state, ok := hostIPToState[hostIP]
		if !ok {
			continue
		}
		if state == common.HOST_STATE_COMPLETE {
			availableHostNum += 1
			hostAvailableVTapNum = append(
				hostAvailableVTapNum,
				common.KVPair{Key: hostIP, Value: availableVTapNum},
			)
		}

		usedVTapNum, ok := hostIPToUsedVTapNum[hostIP]
		if !ok {
			usedVTapNum = 0
		}
		hostIPToRebalanceResult[hostIP] = &model.HostVTapRebalanceResult{
			IP:            hostIP,
			State:         state,
			AZ:            azLcuuid,
			BeforeVTapNum: usedVTapNum,
			AfterVTapNum:  usedVTapNum,
			SwitchVTapNum: 0,
		}
	}

	// 计算平均采集器个数（向上取整），仅考虑状态正常的控制器/数据节点
	avgVTapNum := int(math.Ceil(float64(vtapNum) / float64(availableHostNum)))

	// 超出平均个数的控制器，对其上采集器进行重新分配
	response := model.AZVTapRebalanceResult{}
	for hostIP, vtaps := range hostIPToVTaps {
		hostVTapRebalanceResult, ok := hostIPToRebalanceResult[hostIP]
		if !ok {
			continue
		}

		// 未超出无需进行重新分配
		if len(vtaps) <= avgVTapNum {
			continue
		}

		// 遍历超出部分，进行重新分配
		for i := avgVTapNum; i < len(vtaps); i++ {
			vtap := vtaps[i]

			// 优先分配剩余采集器个数最多的控制器/数据节点
			sort.Slice(hostAvailableVTapNum, func(m, n int) bool {
				return hostAvailableVTapNum[m].Value > hostAvailableVTapNum[n].Value
			})
			hostAvailableVTapNum[0].Value -= 1

			// 判断当前分配的控制器/数据节点是否与原有一致，如果不一致更新result数据
			reallocHostIP := hostAvailableVTapNum[0].Key
			if hostType == "controller" {
				log.Infof(
					"rebalance vtap (%s) controller_ip from (%s) to (%s)",
					vtap.Name, vtap.ControllerIP, reallocHostIP,
				)
				if vtap.ControllerIP == reallocHostIP {
					continue
				}
				if !ifCheck {
					db.Model(vtap).Update("controller_ip", reallocHostIP)
				}
			} else {
				log.Infof(
					"rebalance vtap (%s) analyzer_ip from (%s) to (%s)",
					vtap.Name, vtap.AnalyzerIP, reallocHostIP,
				)
				if vtap.AnalyzerIP == reallocHostIP {
					continue
				}
				if !ifCheck {
					db.Model(vtap).Update("analyzer_ip", reallocHostIP)
				}
			}
			hostVTapRebalanceResult.AfterVTapNum -= 1
			hostVTapRebalanceResult.SwitchVTapNum += 1
			response.TotalSwitchVTapNum += 1

			if newHostVTapRebalanceResult, ok := hostIPToRebalanceResult[reallocHostIP]; ok {
				newHostVTapRebalanceResult.AfterVTapNum += 1
				newHostVTapRebalanceResult.SwitchVTapNum += 1
			}
		}
	}

	for _, hostRebalanceResult := range hostIPToRebalanceResult {
		response.Details = append(response.Details, hostRebalanceResult)
	}
	return response
}

func vtapControllerRebalance(db *mysql.DB, azs []mysql.AZ, ifCheck bool) (*model.VTapRebalanceResult, error) {
	var controllers []mysql.Controller
	var azControllerConns []mysql.AZControllerConnection
	var vtaps []mysql.VTap
	response := &model.VTapRebalanceResult{}

	db.Find(&controllers)
	db.Find(&azControllerConns)
	db.Where("controller_ip != ''").Find(&vtaps)

	azToVTaps := make(map[string][]*mysql.VTap)
	for i, vtap := range vtaps {
		azToVTaps[vtap.AZ] = append(azToVTaps[vtap.AZ], &vtaps[i])
	}

	regionToAZLcuuids := make(map[string][]string)
	for _, az := range azs {
		regionToAZLcuuids[az.Region] = append(regionToAZLcuuids[az.Region], az.Lcuuid)
	}

	normalControllerNum := 0
	ipToController := make(map[string]*mysql.Controller)
	for i, controller := range controllers {
		ipToController[controller.IP] = &controllers[i]
		if controller.State == common.HOST_STATE_COMPLETE && controller.VTapMax > 0 {
			normalControllerNum += 1
		}
	}
	if normalControllerNum == 0 {
		errMsg := "No available controllers，Global equalization is not possible"
		return nil, NewError(httpcommon.SERVER_ERROR, errMsg)
	}

	// 获取各可用区中的控制列表
	azToControllers := make(map[string][]*mysql.Controller)
	for _, conn := range azControllerConns {
		if conn.AZ == "ALL" {
			if azLcuuids, ok := regionToAZLcuuids[conn.Region]; ok {
				for _, azLcuuid := range azLcuuids {
					if controller, ok := ipToController[conn.ControllerIP]; ok {
						azToControllers[azLcuuid] = append(
							azToControllers[azLcuuid], controller,
						)
					}
				}
			}
		} else {
			if controller, ok := ipToController[conn.ControllerIP]; ok {
				azToControllers[conn.AZ] = append(azToControllers[conn.AZ], controller)
			}
		}
	}

	// 遍历可用区，进行控制器均衡
	for _, az := range azs {
		azVTaps, ok := azToVTaps[az.Lcuuid]
		if !ok {
			continue
		}
		azControllers, ok := azToControllers[az.Lcuuid]
		if !ok {
			continue
		}

		// 获取控制器当前已分配的采集器信息
		controllerIPToVTaps := make(map[string][]*mysql.VTap)
		for _, vtap := range azVTaps {
			controllerIPToVTaps[vtap.ControllerIP] = append(
				controllerIPToVTaps[vtap.ControllerIP], vtap,
			)
		}
		// 获取控制器当前剩余可用采集器个数
		controllerIPToState := make(map[string]int)
		controllerIPToAvailableVTapNum := make(map[string]int)
		controllerIPToUsedVTapNum := make(map[string]int)
		for _, controller := range azControllers {
			usedVTapNum := 0
			if controllerVTaps, ok := controllerIPToVTaps[controller.IP]; ok {
				usedVTapNum = len(controllerVTaps)
			}
			controllerIPToState[controller.IP] = controller.State
			controllerIPToUsedVTapNum[controller.IP] = usedVTapNum
			controllerIPToAvailableVTapNum[controller.IP] = controller.VTapMax - usedVTapNum
		}

		// 执行均衡操作
		azVTapRebalanceResult := execAZRebalance(
			db, az.Lcuuid, len(azVTaps), "controller", controllerIPToVTaps,
			controllerIPToAvailableVTapNum, controllerIPToUsedVTapNum,
			controllerIPToState, ifCheck,
		)
		response.TotalSwitchVTapNum += azVTapRebalanceResult.TotalSwitchVTapNum
		response.Details = append(response.Details, azVTapRebalanceResult.Details...)
	}
	return response, nil
}

func vtapAnalyzerRebalance(db *mysql.DB, azs []mysql.AZ, ifCheck bool) (*model.VTapRebalanceResult, error) {
	var analyzers []mysql.Analyzer
	var azAnalyzerConns []mysql.AZAnalyzerConnection
	var vtaps []mysql.VTap
	response := &model.VTapRebalanceResult{}

	db.Find(&analyzers)
	db.Find(&azAnalyzerConns)
	db.Where("analyzer_ip != ''").Find(&vtaps)

	azToVTaps := make(map[string][]*mysql.VTap)
	for i, vtap := range vtaps {
		azToVTaps[vtap.AZ] = append(azToVTaps[vtap.AZ], &vtaps[i])
	}

	regionToAZLcuuids := make(map[string][]string)
	for _, az := range azs {
		regionToAZLcuuids[az.Region] = append(regionToAZLcuuids[az.Region], az.Lcuuid)
	}

	normalAnalyzerNum := 0
	ipToAnalyzer := make(map[string]*mysql.Analyzer)
	for i, analyzer := range analyzers {
		ipToAnalyzer[analyzer.IP] = &analyzers[i]
		if analyzer.State == common.HOST_STATE_COMPLETE && analyzer.VTapMax > 0 {
			normalAnalyzerNum += 1
		}
	}
	if normalAnalyzerNum == 0 {
		errMsg := "No available analyzers，Global equalization is not possible"
		return nil, NewError(httpcommon.SERVER_ERROR, errMsg)
	}

	azToAnalyzers := rebalance.GetAZToAnalyzers(azAnalyzerConns, regionToAZLcuuids, ipToAnalyzer)

	// 遍历可用区，进行数据节点均衡
	for _, az := range azs {
		azVTaps, ok := azToVTaps[az.Lcuuid]
		if !ok {
			continue
		}
		azAnalyzers, ok := azToAnalyzers[az.Lcuuid]
		if !ok {
			continue
		}
		// 获取数据节点当前已分配的采集器信息
		analyzerIPToVTaps := make(map[string][]*mysql.VTap)
		for _, vtap := range azVTaps {
			analyzerIPToVTaps[vtap.AnalyzerIP] = append(
				analyzerIPToVTaps[vtap.AnalyzerIP], vtap,
			)
		}
		// 获取数据节点当前剩余可用采集器个数
		analyzerIPToState := make(map[string]int)
		analyzerIPToAvailableVTapNum := make(map[string]int)
		analyzerIPToUsedVTapNum := make(map[string]int)
		for _, analyzer := range azAnalyzers {
			usedVTapNum := 0
			if analyzerVTaps, ok := analyzerIPToVTaps[analyzer.IP]; ok {
				usedVTapNum = len(analyzerVTaps)
			}
			analyzerIPToState[analyzer.IP] = analyzer.State
			analyzerIPToUsedVTapNum[analyzer.IP] = usedVTapNum
			analyzerIPToAvailableVTapNum[analyzer.IP] = analyzer.VTapMax - usedVTapNum
		}

		// 执行均衡操作
		azVTapRebalanceResult := execAZRebalance(
			db, az.Lcuuid, len(azVTaps), "analyzer", analyzerIPToVTaps,
			analyzerIPToAvailableVTapNum, analyzerIPToUsedVTapNum,
			analyzerIPToState, ifCheck,
		)
		response.TotalSwitchVTapNum += azVTapRebalanceResult.TotalSwitchVTapNum
		response.Details = append(response.Details, azVTapRebalanceResult.Details...)
	}
	return response, nil
}

func VTapRebalance(db *mysql.DB, args map[string]interface{}, cfg monitorconf.IngesterLoadBalancingStrategy) (*model.VTapRebalanceResult, error) {
	var azs []mysql.AZ

	hostType := "controller"
	if argsType, ok := args["type"]; ok {
		hostType = argsType.(string)
	}

	ifCheck := false
	if argsCheck, ok := args["check"]; ok {
		ifCheck = argsCheck.(bool)
	}

	db.Find(&azs)
	if hostType == "controller" {
		return vtapControllerRebalance(db, azs, ifCheck)
	} else {
		if cfg.Algorithm == common.ANALYZER_ALLOC_BY_INGESTED_DATA {
			return rebalance.NewAnalyzerInfo().RebalanceAnalyzerByTraffic(db, ifCheck, cfg.DataDuration)
		} else if cfg.Algorithm == common.ANALYZER_ALLOC_BY_AGENT_COUNT {
			result, err := vtapAnalyzerRebalance(db, azs, ifCheck)
			if err != nil {
				return nil, err
			}
			for _, detail := range result.Details {
				detail.BeforeVTapWeights = 1
				detail.AfterVTapWeights = 1
			}
			return result, nil
		} else {
			return nil, fmt.Errorf("algorithm(%s) is not supported, only supports: %s, %s", cfg.Algorithm,
				common.ANALYZER_ALLOC_BY_INGESTED_DATA, common.ANALYZER_ALLOC_BY_AGENT_COUNT)
		}
	}
}

// GetVTapPortsCount gets the number of virtual network cards covered by the deployed vtap,
// and virtual network type is VIF_DEVICE_TYPE_VM or VIF_DEVICE_TYPE_POD.
func GetVTapPortsCount() (int, error) {
	var vtaps []mysql.VTap
	if err := mysql.Db.Find(&vtaps).Error; err != nil {
		return 0, err
	}
	vtapHostIPs, vtapNodeIPs := mapset.NewSet(), mapset.NewSet()
	pubVTapServers, podVTapServers := mapset.NewSet(), mapset.NewSet()
	for _, vtap := range vtaps {
		if utils.Find([]int{common.VTAP_TYPE_KVM, common.VTAP_TYPE_ESXI}, vtap.Type) {
			vtapHostIPs.Add(vtap.LaunchServer)
		} else if utils.Find([]int{common.VTAP_TYPE_POD_HOST, common.VTAP_TYPE_POD_VM}, vtap.Type) {
			vtapNodeIPs.Add(vtap.LaunchServer)
		} else if utils.Find([]int{common.VTAP_TYPE_WORKLOAD_V}, vtap.Type) {
			pubVTapServers.Add(vtap.LaunchServer)
		} else if utils.Find([]int{common.VTAP_TYPE_K8S_SIDECAR}, vtap.Type) {
			podVTapServers.Add(vtap.LaunchServer)
		}
	}

	var vms []mysql.VM
	if err := mysql.Db.Find(&vms).Error; err != nil {
		return 0, err
	}
	vtapVMIDs := mapset.NewSet()
	for _, vm := range vms {
		if vtapHostIPs.Contains(vm.LaunchServer) {
			vtapVMIDs.Add(vm.ID)
		}
	}

	var podNodes []mysql.PodNode
	if err := mysql.Db.Find(&podNodes).Error; err != nil {
		return 0, err
	}
	podNodeIDs := mapset.NewSet()
	for _, podNode := range podNodes {
		if vtapNodeIPs.Contains(podNode.IP) {
			podNodeIDs.Add(podNode.ID)
		}
	}

	var pods []mysql.Pod
	if err := mysql.Db.Find(&pods).Error; err != nil {
		return 0, err
	}
	vtapPodIDs := mapset.NewSet()
	for _, pod := range pods {
		if podNodeIDs.Contains(pod.PodNodeID) {
			vtapPodIDs.Add(pod.ID)
		}
	}

	var lanIPs []mysql.LANIP
	if err := mysql.Db.Find(&lanIPs).Error; err != nil {
		return 0, err
	}
	pubVTapVIFs := mapset.NewSet()
	for _, lanIP := range lanIPs {
		if pubVTapServers.Contains(lanIP.IP) || podVTapServers.Contains(lanIP.IP) {
			pubVTapVIFs.Add(lanIP.VInterfaceID)
		}
	}

	vtapVifCount := 0
	var vinterfaces []mysql.VInterface
	if err := mysql.Db.Where("devicetype = ? or devicetype = ?", common.VIF_DEVICE_TYPE_VM, common.VIF_DEVICE_TYPE_POD).
		Find(&vinterfaces).Error; err != nil {
		return 0, err
	}
	for _, vif := range vinterfaces {
		if vif.DeviceType == common.VIF_DEVICE_TYPE_VM && pubVTapVIFs.Contains(vif.ID) {
			vtapVifCount++
		} else if vif.DeviceType == common.VIF_DEVICE_TYPE_POD {
			if vtapPodIDs.Contains(vif.DeviceID) || pubVTapVIFs.Contains(vif.ID) {
				vtapVifCount++
			}
		}
	}

	return vtapVifCount, nil
}
