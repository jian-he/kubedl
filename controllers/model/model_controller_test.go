package controllers

import (
	"context"
	"testing"

	"github.com/alibaba/kubedl/apis"
	modelv1alpha1 "github.com/alibaba/kubedl/apis/model/v1alpha1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestCreateModel(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = apis.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	model := createModel("model1")
	fakeClient := fake.NewFakeClientWithScheme(scheme, model)
	//eventBroadcaster := record.NewBroadcaster()
	//recorder := eventBroadcaster.NewRecorder(scheme, corev1.EventSource{Component: "model-controller"})
	modelReconciler := &ModelReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	model1Request := reconcile.Request{NamespacedName: types.NamespacedName{
		Namespace: "default",
		Name:      "model1",
	}}
	_, _ = modelReconciler.Reconcile(model1Request)

	pvc := &corev1.PersistentVolumeClaim{}
	_ = modelReconciler.Get(context.TODO(), types.NamespacedName{
		Namespace: model1Request.Namespace,
		Name:      GetModelPVCName(model.Name),
	}, pvc)
	assert.True(t, pvc.Name == GetModelPVCName(model.Name))
	assert.True(t, pvc.Spec.VolumeName == GetModelPVName(model.Name))

}

func createModel(modelName string) *modelv1alpha1.Model {
	model := &modelv1alpha1.Model{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name:      modelName,
			Namespace: "default",
		},
		Spec: modelv1alpha1.ModelSpec{
			Storage: &modelv1alpha1.Storage{
				LocalStorage: &modelv1alpha1.LocalStorage{
					Path: "/tmp/model",
				},
			},
			ImageRepo: "imageRepo",
		},
		Status: modelv1alpha1.ModelStatus{},
	}
	return model
}
