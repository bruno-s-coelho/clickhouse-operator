// Copyright 2019 Altinity Ltd and/or its affiliates. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package models

import (
	chiv1 "github.com/altinity/clickhouse-operator/pkg/apis/clickhouse.altinity.com/v1"
	"github.com/altinity/clickhouse-operator/pkg/config"
	"github.com/altinity/clickhouse-operator/pkg/util"
	"strconv"

	"github.com/golang/glog"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type Creator struct {
	chi *chiv1.ClickHouseInstallation
	config *config.Config
	appVersion string

	podTemplatesIndex podTemplatesIndex
	volumeClaimTemplatesIndex volumeClaimTemplatesIndex
}

func NewCreator(chi *chiv1.ClickHouseInstallation, config *config.Config, appVersion string) *Creator {
	creator := &Creator{
		chi: chi,
		config: config,
		appVersion: appVersion,
	}
	creator.createPodTemplatesIndex()
	creator.createVolumeClaimTemplatesIndex()

	return creator
}

// ChiCreateObjects returns a map of the k8s objects created based on ClickHouseInstallation Object properties
func (creator *Creator) CreateObjects() []interface{} {
	list := make([]interface{}, 0)
	list = append(list, creator.createServiceObjects())
	list = append(list, creator.createConfigMapObjects())
	list = append(list, creator.createStatefulSetObjects())

	return list
}

// createConfigMapObjects returns a list of corev1.ConfigMap objects
func (creator *Creator) createConfigMapObjects() ConfigMapList {
	configMapList := make(ConfigMapList, 0)
	configMapList = append(
		configMapList,
		creator.createConfigMapObjectsCommon()...,
	)
	configMapList = append(
		configMapList,
		creator.createConfigMapObjectsPod()...,
	)

	return configMapList
}

func (creator *Creator) createConfigMapObjectsCommon() ConfigMapList {
	var configs configSections

	// commonConfigSections maps section name to section XML config of the following sections:
	// 1. remote servers
	// 2. zookeeper
	// 3. settings
	// 4. listen
	configs.commonConfigSections = make(map[string]string)
	util.IncludeNonEmpty(configs.commonConfigSections, filenameRemoteServersXML, generateRemoteServersConfig(creator.chi))
	util.IncludeNonEmpty(configs.commonConfigSections, filenameZookeeperXML, generateZookeeperConfig(creator.chi))
	util.IncludeNonEmpty(configs.commonConfigSections, filenameSettingsXML, generateSettingsConfig(creator.chi))
	util.IncludeNonEmpty(configs.commonConfigSections, filenameListenXML, generateListenConfig(creator.chi))
	// Extra user-specified configs
	for filename, content := range creator.config.ChCommonConfigs {
		util.IncludeNonEmpty(configs.commonConfigSections, filename, content)
	}

	// commonConfigSections maps section name to section XML config of the following sections:
	// 1. users
	// 2. quotas
	// 3. profiles
	configs.commonUsersConfigSections = make(map[string]string)
	util.IncludeNonEmpty(configs.commonUsersConfigSections, filenameUsersXML, generateUsersConfig(creator.chi))
	util.IncludeNonEmpty(configs.commonUsersConfigSections, filenameQuotasXML, generateQuotasConfig(creator.chi))
	util.IncludeNonEmpty(configs.commonUsersConfigSections, filenameProfilesXML, generateProfilesConfig(creator.chi))
	// Extra user-specified configs
	for filename, content := range creator.config.ChUsersConfigs {
		util.IncludeNonEmpty(configs.commonUsersConfigSections, filename, content)
	}

	// There are two types of configs, kept in ConfigMaps:
	// 1. Common configs - for all resources in the CHI (remote servers, zookeeper setup, etc)
	//    consists of common configs and common users configs
	// 2. Personal configs - macros config
	// configMapList contains all configs so we need deploymentsNum+2 ConfigMap objects
	// personal config for each deployment and +2 for common config + common user config
	configMapList := make(ConfigMapList, 0)

	// ConfigMap common for all resources in CHI
	// contains several sections, mapped as separated config files,
	// such as remote servers, zookeeper setup, etc
	configMapList = append(
		configMapList,
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      CreateConfigMapCommonName(creator.chi.Name),
				Namespace: creator.chi.Namespace,
				Labels: map[string]string{
					ChopGeneratedLabel: creator.appVersion,
					ChiGeneratedLabel:  creator.chi.Name,
				},
			},
			// Data contains several sections which are to be several xml configs
			Data: configs.commonConfigSections,
		},
	)

	// ConfigMap common for all users resources in CHI
	configMapList = append(
		configMapList,
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      CreateConfigMapCommonUsersName(creator.chi.Name),
				Namespace: creator.chi.Namespace,
				Labels: map[string]string{
					ChopGeneratedLabel: creator.appVersion,
					ChiGeneratedLabel:  creator.chi.Name,
				},
			},
			// Data contains several sections which are to be several xml configs
			Data: configs.commonUsersConfigSections,
		},
	)

	return configMapList
}

func (creator *Creator) createConfigMapObjectsPod() ConfigMapList {
	configMapList := make(ConfigMapList, 0)
	replicaProcessor := func(replica *chiv1.ChiClusterLayoutShardReplica) error {
		// Prepare for this replica deployment config files map as filename->content
		podConfigSections := make(map[string]string)
		util.IncludeNonEmpty(podConfigSections, filenameMacrosXML, generateHostMacros(replica))
		// Extra user-specified configs
		for filename, content := range creator.config.ChPodConfigs {
			util.IncludeNonEmpty(podConfigSections, filename, content)
		}

		// Add corev1.ConfigMap object to the list
		configMapList = append(
			configMapList,
			&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      CreateConfigMapPodName(replica),
					Namespace: replica.Address.Namespace,
					Labels: map[string]string{
						ChopGeneratedLabel: creator.appVersion,
						ChiGeneratedLabel:  replica.Address.ChiName,
					},
				},
				Data: podConfigSections,
			},
		)

		return nil
	}
	creator.chi.WalkReplicas(replicaProcessor)

	return configMapList
}

// createServiceObjects returns a list of corev1.Service objects
func (creator *Creator) createServiceObjects() ServiceList {
	// We'd like to create "number of deployments" + 1 kubernetes services in order to provide access
	// to each deployment separately and one common predictably-named access point - common service
	serviceList := make(ServiceList, 0)
	serviceList = append(
		serviceList,
		creator.createServiceObjectsCommon()...,
	)
	serviceList = append(
		serviceList,
		creator.createServiceObjectsPod()...,
	)

	return serviceList
}

func (creator *Creator) createServiceObjectsCommon() ServiceList {
	// Create one predictably-named service to access the whole installation
	// NAME                             TYPE        CLUSTER-IP   EXTERNAL-IP   PORT(S)                      AGE
	// service/clickhouse-replcluster   ClusterIP   None         <none>        9000/TCP,9009/TCP,8123/TCP   1h
	return ServiceList{
		creator.createServiceObjectChi(CreateChiServiceName(creator.chi)),
	}
}

func (creator *Creator) createServiceObjectsPod() ServiceList {
	// Create "number of pods" service - one service for each stateful set
	// Each replica has its stateful set and each stateful set has it service
	// NAME                             TYPE        CLUSTER-IP   EXTERNAL-IP   PORT(S)                      AGE
	// service/chi-01a1ce7dce-2         ClusterIP   None         <none>        9000/TCP,9009/TCP,8123/TCP   1h
	serviceList := make(ServiceList, 0)

	replicaProcessor := func(replica *chiv1.ChiClusterLayoutShardReplica) error {
		// Add corev1.Service object to the list
		serviceList = append(
			serviceList,
			creator.createServiceObjectPod(replica),
		)
		return nil
	}
	creator.chi.WalkReplicas(replicaProcessor)

	return serviceList
}

func (creator *Creator) createServiceObjectChi(serviceName string) *corev1.Service {
	glog.Infof("createServiceObjectChi() for service %s\n", serviceName)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: creator.chi.Namespace,
			Labels: map[string]string{
				ChopGeneratedLabel: creator.appVersion,
				ChiGeneratedLabel:  creator.chi.Name,
			},
		},
		Spec: corev1.ServiceSpec{
			// ClusterIP: templateDefaultsServiceClusterIP,
			Ports: []corev1.ServicePort{
				{
					Name: chDefaultHTTPPortName,
					Port: chDefaultHTTPPortNumber,
				},
				{
					Name: chDefaultClientPortName,
					Port: chDefaultClientPortNumber,
				},
			},
			Selector: map[string]string{
				ChiGeneratedLabel: creator.chi.Name,
			},
			Type: "LoadBalancer",
		},
	}
}

func (creator *Creator) createServiceObjectPod(replica *chiv1.ChiClusterLayoutShardReplica) *corev1.Service {
	serviceName := CreateStatefulSetServiceName(replica)
	statefulSetName := CreateStatefulSetName(replica)

	glog.Infof("createServiceObjectPod() for service %s %s\n", serviceName, statefulSetName)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: replica.Address.Namespace,
			Labels: map[string]string{
				ChopGeneratedLabel:         creator.appVersion,
				ChiGeneratedLabel:          replica.Address.ChiName,
				ClusterGeneratedLabel:      replica.Address.ClusterName,
				ClusterIndexGeneratedLabel: strconv.Itoa(replica.Address.ClusterIndex),
				ReplicaIndexGeneratedLabel: strconv.Itoa(replica.Address.ReplicaIndex),
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name: chDefaultHTTPPortName,
					Port: chDefaultHTTPPortNumber,
				},
				{
					Name: chDefaultClientPortName,
					Port: chDefaultClientPortNumber,
				},
				{
					Name: chDefaultInterServerPortName,
					Port: chDefaultInterServerPortNumber,
				},
			},
			Selector: map[string]string{
				chDefaultAppLabel: statefulSetName,
			},
			ClusterIP: templateDefaultsServiceClusterIP,
			Type:      "ClusterIP",
		},
	}
}

func IsChopGeneratedObject(objectMeta *metav1.ObjectMeta) bool {
	// Parse Labels
	// 			Labels: map[string]string{
	//				ChopGeneratedLabel: AppVersion,
	//				ChiGeneratedLabel:  replica.Address.ChiName,
	//				ClusterGeneratedLabel: replica.Address.ClusterName,
	//				ClusterIndexGeneratedLabel: strconv.Itoa(replica.Address.ClusterIndex),
	//				ReplicaIndexGeneratedLabel: strconv.Itoa(replica.Address.ReplicaIndex),
	//			},

	// ObjectMeta must have some labels
	if len(objectMeta.Labels) == 0 {
		return false
	}

	// ObjectMeta must have ChopGeneratedLabel
	_, ok := objectMeta.Labels[ChopGeneratedLabel]

	return ok
}

// createStatefulSetObjects returns a list of apps.StatefulSet objects
func (creator *Creator) createStatefulSetObjects() StatefulSetList {
	statefulSetList := make(StatefulSetList, 0)

	// Create list of apps.StatefulSet objects
	// StatefulSet is created for each replica.Deployment

	replicaProcessor := func(replica *chiv1.ChiClusterLayoutShardReplica) error {
		glog.Infof("createStatefulSetObjects() for statefulSet %s\n", CreateStatefulSetName(replica))
		// Append new StatefulSet to the list of stateful sets
		statefulSetList = append(statefulSetList, creator.createStatefulSetObject(replica))
		return nil
	}
	creator.chi.WalkReplicas(replicaProcessor)

	return statefulSetList
}

func (creator *Creator) createStatefulSetObject(replica *chiv1.ChiClusterLayoutShardReplica) *apps.StatefulSet {
	statefulSetName := CreateStatefulSetName(replica)
	serviceName := CreateStatefulSetServiceName(replica)

	// Create apps.StatefulSet object
	replicasNum := int32(1)
	statefulSet := &apps.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      statefulSetName,
			Namespace: replica.Address.Namespace,
			Labels: map[string]string{
				ChopGeneratedLabel: creator.appVersion,
				ChiGeneratedLabel:  replica.Address.ChiName,
				ZkVersionLabel:     replica.Config.ZkFingerprint,
			},
		},
		Spec: apps.StatefulSetSpec{
			Replicas:    &replicasNum,
			ServiceName: serviceName,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					chDefaultAppLabel: statefulSetName,
				},
			},
			// IMPORTANT
			// VolumeClaimTemplates are to be setup later
			VolumeClaimTemplates: nil,

			// IMPORTANT
			// Template to be setup later
			Template: corev1.PodTemplateSpec{},
		},
	}

	creator.setupStatefulSetPodTemplate(statefulSet, replica)
	creator.setupStatefulSetVolumeClaimTemplate(statefulSet, replica)

	return statefulSet
}

func (creator *Creator) findPodTemplate(podtemplate string) (*corev1.PodTemplateSpec, bool) {
	for i := range creator.chi.
}

func (creator *Creator) setupStatefulSetPodTemplate(
	statefulSetObject *apps.StatefulSet,
	replica *chiv1.ChiClusterLayoutShardReplica,
) {
	statefulSetName := CreateStatefulSetName(replica)
	configMapMacrosName := CreateConfigMapPodName(replica)
	configMapCommonName := CreateConfigMapCommonName(replica.Address.ChiName)
	configMapCommonUsersName := CreateConfigMapCommonUsersName(replica.Address.ChiName)
	podTemplateName := replica.PodTemplate

	statefulSetObject.Spec.Template = corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				chDefaultAppLabel:  statefulSetName,
				ChopGeneratedLabel: creator.appVersion,
				ChiGeneratedLabel:  replica.Address.ChiName,
				ZkVersionLabel:     replica.Config.ZkFingerprint,
			},
		},
	}

	// Specify pod templates - either explicitly defined or default
	if podTemplate, ok := creator.getPodTemplate(podTemplateName); ok {
		// Replica references known PodTemplate
		copyPodTemplateFrom(statefulSetObject, podTemplate)
		glog.Infof("createStatefulSetObjects() for statefulSet %s - template: %s\n", statefulSetName, podTemplateName)
	} else {
		// Replica references UNKNOWN PodTemplate
		copyPodTemplateFrom(statefulSetObject, createDefaultPodTemplate(statefulSetName))
		glog.Infof("createStatefulSetObjects() for statefulSet %s - default template\n", statefulSetName)
	}

	// And now loop over all containers in this template and
	// apply configuration - meaning append all VolumeMounts which are ConfigMap mounts
	for i := range statefulSetObject.Spec.Template.Spec.Containers {
		// Convenience wrapper
		container := &statefulSetObject.Spec.Template.Spec.Containers[i]
		// Append to each Container current VolumeMount's to VolumeMount's declared in template
		container.VolumeMounts = append(
			container.VolumeMounts,
			createVolumeMountObject(configMapCommonName, dirPathConfigd),
			createVolumeMountObject(configMapCommonUsersName, dirPathUsersd),
			createVolumeMountObject(configMapMacrosName, dirPathConfd),
		)
	}

	// Add all ConfigMap objects as Pod's volumes
	statefulSetObject.Spec.Template.Spec.Volumes = append(
		statefulSetObject.Spec.Template.Spec.Volumes,
		createVolumeObjectConfigMap(configMapCommonName),
		createVolumeObjectConfigMap(configMapCommonUsersName),
		createVolumeObjectConfigMap(configMapMacrosName),
	)
}

func (creator *Creator) setupStatefulSetVolumeClaimTemplates(
	statefulSetObject *apps.StatefulSet,
	replica *chiv1.ChiClusterLayoutShardReplica,
) {
	statefulSetName := CreateStatefulSetName(replica)
	for i := range statefulSetObject.Spec.Template.Spec.Containers {
		container := &statefulSetObject.Spec.Template.Spec.Containers[i]
		for j := range container.VolumeMounts {
			volumeMount := &container.VolumeMounts[j]
			if volumeClaimTemplate, ok := creator.getVolumeClaimTemplate(volumeMount.Name); ok {
				// Found VolumeClaimTemplate to mount by VolumeMount
				appendVolumeClaimTemplateFrom(statefulSetObject, volumeClaimTemplate)
			}
		}
	}


	// This volumeClaimTemplate may be used as
	// 1. explicitly in VolumeMounts
	// 2. implicitly as default volume for /var/lib/clickhouse
	// So we need to check, whether this volumeClaimTemplate should be used as default volume mounted at /var/lib/clickhouse

	// 1. Check explicit usage
	// This volumeClaimTemplate may be explicitly used already, in case
	// it is explicitly mentioned in Container's VolumeMounts.
	for i := range statefulSetObject.Spec.Template.Spec.Containers[ClickHouseContainerIndex].VolumeMounts {
		// Convenience wrapper
		volumeMount := &statefulSetObject.Spec.Template.Spec.Containers[ClickHouseContainerIndex].VolumeMounts[i]
		if volumeMount.Name == volumeClaimTemplate.PersistentVolumeClaim.Name {
			// This volumeClaimTemplate is already used
			glog.Infof("createStatefulSetObjects() for statefulSet %s - VC template 1: %s\n", statefulSetName, volumeClaimTemplateName)
			return
		}
	}

	// 2. Check /var/lib/clickhouse usage
	// This volumeClaimTemplate is not used by name - it is unused - what's it's purpose, then?
	// So we want to mount it to /var/lib/clickhouse even more now, because it is unused.
	// However, mount point /var/lib/clickhouse may be used already explicitly. Need to check
	for i := range statefulSetObject.Spec.Template.Spec.Containers[ClickHouseContainerIndex].VolumeMounts {
		// Convenience wrapper
		volumeMount := &statefulSetObject.Spec.Template.Spec.Containers[ClickHouseContainerIndex].VolumeMounts[i]
		if volumeMount.MountPath == dirPathClickHouseData {
			// /var/lib/clickhouse is already mounted
			glog.Infof("createStatefulSetObjects() for statefulSet %s - VC template 1: %s\n", statefulSetName, volumeClaimTemplateName)
			return
		}
	}

	// This volumeClaimTemplate is not used by name and /var/lib/clickhouse is not mounted
	// Let's mount this volumeClaimTemplate into /var/lib/clickhouse
	statefulSetObject.Spec.Template.Spec.Containers[ClickHouseContainerIndex].VolumeMounts = append(
		statefulSetObject.Spec.Template.Spec.Containers[ClickHouseContainerIndex].VolumeMounts,
		corev1.VolumeMount{
			Name:      volumeClaimTemplate.PersistentVolumeClaim.Name,
			MountPath: dirPathClickHouseData,
		},
	)

	glog.Infof("createStatefulSetObjects() for statefulSet %s - VC template.useDefaultName: %s\n", statefulSetName, volumeClaimTemplateName)
}

func copyPodTemplateFrom(dst *apps.StatefulSet, src *chiv1.ChiPodTemplate) {
	dst.Spec.Template.Name = src.Name
	dst.Spec.Template.Spec = *src.Spec.DeepCopy()
}

func appendVolumeClaimTemplateFrom(dst *apps.StatefulSet, src *chiv1.ChiVolumeClaimTemplate) {
	if dst.Spec.VolumeClaimTemplates == nil {
		dst.Spec.VolumeClaimTemplates = make([]corev1.PersistentVolumeClaim, 0, 0)
	}
	dst.Spec.VolumeClaimTemplates = append(dst.Spec.VolumeClaimTemplates, corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: src.Name,
		},
		Spec: *src.Spec.DeepCopy(),
	})
}

// createDefaultPodTemplate returns default Pod Template to be used with StatefulSet
func createDefaultPodTemplate(name string) *chiv1.ChiPodTemplate {
	return &chiv1.ChiPodTemplate{
		Name: name,
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "clickhouse",
					Image: defaultClickHouseDockerImage,
					Ports: []corev1.ContainerPort{
						{
							Name:          chDefaultHTTPPortName,
							ContainerPort: chDefaultHTTPPortNumber,
						},
						{
							Name:          chDefaultClientPortName,
							ContainerPort: chDefaultClientPortNumber,
						},
						{
							Name:          chDefaultInterServerPortName,
							ContainerPort: chDefaultInterServerPortNumber,
						},
					},
					ReadinessProbe: &corev1.Probe{
						Handler: corev1.Handler{
							HTTPGet: &corev1.HTTPGetAction{
								Path: "/ping",
								Port: intstr.Parse(chDefaultHTTPPortName),
							},
						},
						InitialDelaySeconds: 10,
						PeriodSeconds:       10,
					},
				},
			},
			Volumes: []corev1.Volume{},
		},
	}
}

// createVolumeObjectConfigMap returns corev1.Volume object with defined name
func createVolumeObjectConfigMap(name string) corev1.Volume {
	return corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: name,
				},
			},
		},
	}
}

// createVolumeMountObject returns corev1.VolumeMount object with name and mount path
func createVolumeMountObject(name, mountPath string) corev1.VolumeMount {
	return corev1.VolumeMount{
		Name:      name,
		MountPath: mountPath,
	}
}

// createVolumeClaimTemplatesIndex creates a map of volumeClaimTemplatesIndexData used as a reference storage for VolumeClaimTemplates
func (creator *Creator) createVolumeClaimTemplatesIndex() {
	creator.volumeClaimTemplatesIndex = make(volumeClaimTemplatesIndex)
	for i := range creator.chi.Spec.Templates.VolumeClaimTemplates {
		// Convenience wrapper
		volumeClaimTemplate := &creator.chi.Spec.Templates.VolumeClaimTemplates[i]
		creator.volumeClaimTemplatesIndex[volumeClaimTemplate.Name] = volumeClaimTemplate
	}
}

// getVolumeClaimTemplate gets VolumeClaimTemplate by name
func (creator *Creator) getVolumeClaimTemplate(name string) (*chiv1.ChiVolumeClaimTemplate, bool) {
	pvc, ok := creator.volumeClaimTemplatesIndex[name]
	return pvc, ok
}

// createPodTemplatesIndex creates a map of podTemplatesIndexData used as a reference storage for PodTemplates
func (creator *Creator) createPodTemplatesIndex() {
	creator.podTemplatesIndex = make(podTemplatesIndex)
	for i := range creator.chi.Spec.Templates.PodTemplates {
		// Convenience wrapper
		podTemplate := &creator.chi.Spec.Templates.PodTemplates[i]
		creator.podTemplatesIndex[podTemplate.Name] = podTemplate
	}
}

// getPodTemplate gets PodTemplate by name
func (creator *Creator) getPodTemplate(name string) (*chiv1.ChiPodTemplate, bool) {
	podTemplateSpec, ok := creator.podTemplatesIndex[name]
	return podTemplateSpec, ok
}
