/*
Copyright 2020 The Alibaba Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"time"

	modelv1alpha1 "github.com/alibaba/kubedl/apis/model/v1alpha1"
	"github.com/alibaba/kubedl/controllers/model/storage"
	"github.com/go-logr/logr"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// ModelVersionReconciler reconciles a ModelVersion object
type ModelVersionReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=model.kubedl.io,resources=modelversions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=model.kubedl.io,resources=modelversions/status,verbs=get;update;patch

// ModelVersionController tries to create a kaniko container for each ModelVersion that triggers an image build process.
// If a ModelVersion has its own storage backend defined, it'll also create the underlying pv and pvc first.
func (r *ModelVersionReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	//Get the modelVersion
	modelVersion := &modelv1alpha1.ModelVersion{}
	err := r.Get(context.Background(), req.NamespacedName, modelVersion)
	if err != nil {
		if errors.IsNotFound(err) {
			r.Log.Info("modelVersion doesn't exist", "name", req.String())
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	// no need to build
	if modelVersion.Status.ImageBuildPhase == modelv1alpha1.ImageBuildSucceeded ||
		modelVersion.Status.ImageBuildPhase == modelv1alpha1.ImageBuildFailed {
		return reconcile.Result{}, nil
	}

	// Does the model exist
	model := &modelv1alpha1.Model{}
	err = r.Get(context.Background(), types.NamespacedName{
		Namespace: modelVersion.Namespace,
		Name:      modelVersion.Spec.ModelName,
	}, model)
	if err != nil {
		if errors.IsNotFound(err) {
			r.Log.Info("model doesn't exist", "name", modelVersion.Spec.ModelName)
			return reconcile.Result{}, err
		}
		return reconcile.Result{}, err
	}

	pv := &v1.PersistentVolume{}
	pvc := &v1.PersistentVolumeClaim{}

	if modelVersion.Spec.Storage == nil {
		// Use the storage defined from the parent model
		// Does the pvc for the model exist
		err = r.Get(context.Background(), types.NamespacedName{Namespace: model.Namespace, Name: GetModelPVCName(model.Name)}, pvc)
		if err != nil {
			return reconcile.Result{}, err
		}

		// Does the pv for the model exist
		err = r.Get(context.Background(), types.NamespacedName{Name: GetModelPVName(model.Name)}, pv)
		if err != nil {
			return reconcile.Result{}, err
		}
	} else {
		// The modelVersion has its own storage defined
		// Does the pv for the model version already exist
		pvName := ""
		if modelVersion.Spec.Storage.LocalStorage != nil {
			//  append the nodeName
			if modelVersion.Spec.Storage.LocalStorage.NodeName != "" {
				pvName = GetModelVersionPVName(modelVersion.Name) + "-" + modelVersion.Spec.Storage.LocalStorage.NodeName
			} else if model.Spec.Storage.LocalStorage.NodeName != "" {
				pvName = GetModelVersionPVName(modelVersion.Name) + "-" + model.Spec.Storage.LocalStorage.NodeName
			} else {
				return ctrl.Result{}, fmt.Errorf("both model and modelVersion don't have nodeName set for local storage, modelVersion name %s", modelVersion.Name)
			}
		} else {
			pvName = GetModelVersionPVName(modelVersion.Name)
		}
		err = r.Get(context.Background(), types.NamespacedName{Name: pvName}, pv)
		if err != nil {
			if errors.IsNotFound(err) {
				// create a new pv
				storageProvider := storage.GetStorageProvider(modelVersion.Spec.Storage)
				newPV := storageProvider.CreatePersistentVolume(modelVersion.Spec.Storage, GetModelVersionPVName(modelVersion.Name))
				if newPV.OwnerReferences == nil {
					pv.OwnerReferences = make([]metav1.OwnerReference, 0)
				}
				pv.OwnerReferences = append(pv.OwnerReferences, metav1.OwnerReference{
					APIVersion: model.APIVersion,
					Kind:       model.Kind,
					Name:       model.Name,
					UID:        model.UID,
				})
				err := r.Create(context.Background(), newPV)
				if err != nil {
					return ctrl.Result{}, err
				}
				log.Info("created pv for model version", "pv", newPV.Name, "model-version", modelVersion.Name)
			}
		} else {
			return reconcile.Result{}, err
		}

		// Does the pvc for the version already exist
		err = r.Get(context.Background(), types.NamespacedName{Namespace: model.Namespace, Name: pvName}, pvc)
		if err != nil {
			if errors.IsNotFound(err) {
				// create the pvc to the pv
				pvcName := ""
				if modelVersion.Spec.Storage.LocalStorage != nil {
					// append the nodeName
					if modelVersion.Spec.Storage.LocalStorage.NodeName != "" {
						pvcName = GetModelVersionPVCName(modelVersion.Name) + "-" + modelVersion.Spec.Storage.LocalStorage.NodeName
					} else if model.Spec.Storage.LocalStorage.NodeName != "" {
						pvcName = GetModelVersionPVCName(modelVersion.Name) + "-" + model.Spec.Storage.LocalStorage.NodeName
					} else {
						return ctrl.Result{}, fmt.Errorf("both model and modelVersion don't have nodeName set for local storage, modelVersion name %s", modelVersion.Name)
					}
				} else {
					pvcName = GetModelVersionPVName(modelVersion.Name)
				}
				pvc = createPVC(pv, pvcName, modelVersion.Namespace)
				if pvc.OwnerReferences == nil {
					pvc.OwnerReferences = make([]metav1.OwnerReference, 0)
				}
				pvc.OwnerReferences = append(pvc.OwnerReferences, metav1.OwnerReference{
					APIVersion: modelVersion.APIVersion,
					Kind:       modelVersion.Kind,
					Name:       modelVersion.Name,
					UID:        modelVersion.UID,
				})
				err = r.Create(context.Background(), pvc)
				if err != nil {
					return ctrl.Result{}, err
				}
				log.Info("created pvc for model", "pvc", pvc.Name, "model", model.Name)
			} else {
				return reconcile.Result{}, err
			}
		}
	}

	// check if the pvc is bound
	if pvc.Status.Phase != v1.ClaimBound {
		// wait for the pv and pvc to be bound
		return ctrl.Result{Requeue: true, RequeueAfter: 1 * time.Second}, nil
	}

	modelVersionStatus := modelVersion.Status.DeepCopy()

	// create kaniko pod to build container image\
	// take the preceding 5 chars for image versionId
	versionId := string(modelVersion.UID[:5])
	imgBuildPodName := GetBuildImagePodName(model.Name, versionId)
	imgBuildPod := &v1.Pod{}
	err = r.Get(context.Background(), types.NamespacedName{Namespace: model.Namespace, Name: imgBuildPodName}, imgBuildPod)
	if err != nil {
		if errors.IsNotFound(err) {
			modelVersionStatus.Image = model.Spec.ImageRepo + ":v" + versionId
			modelVersionStatus.ImageBuildPhase = modelv1alpha1.ImageBuilding
			modelVersionStatus.Message = "Image building started."
			imgBuildPod = createImgBuildPod(modelVersion, pvc, imgBuildPodName, modelVersionStatus.Image)
			err = r.Create(context.Background(), imgBuildPod)
			if err != nil {
				return ctrl.Result{}, err
			}
			if modelVersion.Labels == nil {
				modelVersion.Labels = make(map[string]string,1)
			}
			modelVersion.Labels["model.kubedl.io/model-name"] = model.Name
			if modelVersion.Annotations == nil {
				modelVersion.Annotations = make(map[string]string,1)
			}
			modelVersion.Annotations["model.kubedl.io/img-build-pod-name"] = imgBuildPodName
			modelVersion.Status = *modelVersionStatus
			// update model version
			err = r.Status().Update(context.Background(), modelVersion)

			// update parent model latest version info
			model.Status.LatestVersion = &modelv1alpha1.VersionInfo{
				ModelVersion: modelVersion.Name,
				ImageName:    modelVersionStatus.Image,
			}
			err = r.Status().Update(context.Background(), model)
			return ctrl.Result{RequeueAfter: 2}, err
		} else {
			return ctrl.Result{}, err
		}
	}

	currentTime := metav1.Now()
	if imgBuildPod.Status.Phase == v1.PodSucceeded {
		modelVersionStatus.ImageBuildPhase = modelv1alpha1.ImageBuildSucceeded
		modelVersionStatus.Message = fmt.Sprintf("Image build succeeded.")
		modelVersionStatus.FinishTime = &currentTime
	} else if imgBuildPod.Status.Phase == v1.PodFailed {
		modelVersionStatus.ImageBuildPhase = modelv1alpha1.ImageBuildFailed
		modelVersionStatus.FinishTime = &currentTime
		modelVersionStatus.Message = fmt.Sprintf("Image build failed.")
	} else {
		// image not ready
		return ctrl.Result{RequeueAfter: 1}, nil
	}
	// image ready
	err = r.Status().Update(context.Background(), modelVersion)
	return ctrl.Result{}, err
}

// createImgBuildPod creates a kaniko pod to build the image
// 1. mount docker file into /workspace/build/dockerfile
// 2. mount build source pvc into /workspace/build
// 3. mount dockerconfig as /kaniko/.docker/config.json
func createImgBuildPod(model *modelv1alpha1.ModelVersion, pvc *v1.PersistentVolumeClaim, imgBuildPodName string, newImage string) *v1.Pod {
	podSpec := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      imgBuildPodName,
			Namespace: model.Namespace,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  imgBuildPodName,
					Image: "gcr.io/kaniko-project/executor:latest",
					Args: []string{
						"--dockerfile=/workspace/build/dockerfile",
						"--context=dir://workspace/build",
						fmt.Sprintf("--destination=%s", newImage)},
				},
			},
			RestartPolicy: v1.RestartPolicyNever,
		},
	}
	var volumeMounts = []v1.VolumeMount{
		{
			Name:      "kaniko-secret", // the docker secret for pushing images
			MountPath: "/kaniko/.docker",
		},
		{
			Name:      "build-source", // build-source references the pvc for the model
			MountPath: "/workspace/build",
		},
		{
			Name:      "dockerfile", // dockerfile is the default Dockerfile for building the image including the model
			MountPath: "/workspace/build",
		},
	}
	var volumes = []v1.Volume{
		{
			Name: "kaniko-secret",
			VolumeSource: v1.VolumeSource{
				Secret: &v1.SecretVolumeSource{
					SecretName: "regcred",
					Items: []v1.KeyToPath{
						{
							Key:  ".dockerconfigjson",
							Path: "config.json",
						},
					},
				},
			},
		},
		{
			Name: "build-source",
			VolumeSource: v1.VolumeSource{
				PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvc.Name,
				},
			},
		},
		{
			Name: "dockerfile",
			VolumeSource: v1.VolumeSource{
				ConfigMap: &v1.ConfigMapVolumeSource{
					LocalObjectReference: v1.LocalObjectReference{
						Name: "dockerfile",
					},
					Items: []v1.KeyToPath{
						{
							Key:  "dockerfile",
							Path: "dockerfile",
						},
					},
				},
			},
		},
	}

	podSpec.Spec.Containers[0].VolumeMounts = volumeMounts
	podSpec.Spec.Volumes = volumes
	podSpec.OwnerReferences = append(podSpec.OwnerReferences, metav1.OwnerReference{
		APIVersion: model.APIVersion,
		Kind:       model.Kind,
		Name:       model.Name,
		UID:        model.UID,
	})
	return podSpec
}

func (r *ModelVersionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	var predicates = predicate.Funcs{
		CreateFunc: func(event event.CreateEvent) bool {
			version := event.Meta.(*modelv1alpha1.ModelVersion)
			if version.DeletionTimestamp != nil {
				return false
			}

			return true
		},

		//DeleteFunc: func(deleteEvent event.DeleteEvent) bool {
		//	modelVersion := deleteEvent.Meta.(*modelv1alpha1.ModelVersion)
		//
		//	// delete image build pod
		//	pod := &v1.Pod{
		//		ObjectMeta: metav1.ObjectMeta{
		//			Namespace: modelVersion.Namespace,
		//			Name: modelVersion.Annotations["kubedl.io/img-build-pod-name"]},
		//	}
		//	err := r.Delete(context.Background(), pod)
		//	if err != nil {
		//		log.Error(err, "failed to delete pod "+pod.Name)
		//	}
		//	return true
		//},
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&modelv1alpha1.ModelVersion{}, builder.WithPredicates(predicates)).
		Complete(r)
}
