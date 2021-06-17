package addon

import (
	"context"
	"testing"
	"time"

	addonv1alpha1 "github.com/open-cluster-management/api/addon/v1alpha1"
	addonfake "github.com/open-cluster-management/api/client/addon/clientset/versioned/fake"
	addoninformers "github.com/open-cluster-management/api/client/addon/informers/externalversions"
	testinghelpers "github.com/open-cluster-management/registration/pkg/helpers/testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/clock"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

var now = time.Now()

func TestQueueKeyFunc(t *testing.T) {
	cases := []struct {
		name             string
		addOns           []runtime.Object
		lease            runtime.Object
		expectedQueueKey string
	}{
		{
			name:             "no addons",
			addOns:           []runtime.Object{},
			lease:            testinghelpers.NewAddOnLease("test", "test", time.Now()),
			expectedQueueKey: "",
		},
		{
			name: "no install namespace",
			addOns: []runtime.Object{&addonv1alpha1.ManagedClusterAddOn{
				ObjectMeta: metav1.ObjectMeta{Namespace: testinghelpers.TestManagedClusterName, Name: "test"},
			}},
			lease:            testinghelpers.NewAddOnLease("test", "test", time.Now()),
			expectedQueueKey: "",
		},
		{
			name: "different install namespace",
			addOns: []runtime.Object{&addonv1alpha1.ManagedClusterAddOn{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testinghelpers.TestManagedClusterName,
					Name:      "test",
				},
				Spec: addonv1alpha1.ManagedClusterAddOnSpec{
					InstallNamespace: "other",
				},
			}},
			lease:            testinghelpers.NewAddOnLease("test", "test", time.Now()),
			expectedQueueKey: "",
		},
		{
			name: "an addon lease",
			addOns: []runtime.Object{&addonv1alpha1.ManagedClusterAddOn{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testinghelpers.TestManagedClusterName,
					Name:      "test",
				},
				Spec: addonv1alpha1.ManagedClusterAddOnSpec{
					InstallNamespace: "test",
				},
			}},
			lease:            testinghelpers.NewAddOnLease("test", "test", time.Now()),
			expectedQueueKey: "test/test",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addOnClient := addonfake.NewSimpleClientset(c.addOns...)
			addOnInformerFactory := addoninformers.NewSharedInformerFactory(addOnClient, time.Minute*10)
			addOnStroe := addOnInformerFactory.Addon().V1alpha1().ManagedClusterAddOns().Informer().GetStore()
			for _, addOn := range c.addOns {
				addOnStroe.Add(addOn)
			}

			ctrl := &managedClusterAddOnLeaseController{
				clusterName: testinghelpers.TestManagedClusterName,
				addOnLister: addOnInformerFactory.Addon().V1alpha1().ManagedClusterAddOns().Lister(),
			}
			actualQueueKey := ctrl.queueKeyFunc(c.lease)
			if actualQueueKey != c.expectedQueueKey {
				t.Errorf("expected queue key %q, but got %q", c.expectedQueueKey, actualQueueKey)
			}
		})
	}
}

func TestSync(t *testing.T) {
	cases := []struct {
		name            string
		queueKey        string
		addOns          []runtime.Object
		hubLeases       []runtime.Object
		leases          []runtime.Object
		validateActions func(t *testing.T, ctx *testinghelpers.FakeSyncContext, actions []clienttesting.Action)
	}{
		{
			name:      "bad queue key",
			queueKey:  "test/test/test",
			addOns:    []runtime.Object{},
			hubLeases: []runtime.Object{},
			leases:    []runtime.Object{},
			validateActions: func(t *testing.T, ctx *testinghelpers.FakeSyncContext, actions []clienttesting.Action) {
				testinghelpers.AssertNoActions(t, actions)
			},
		},
		{
			name:      "no addons",
			queueKey:  "test/test",
			addOns:    []runtime.Object{},
			hubLeases: []runtime.Object{},
			leases:    []runtime.Object{},
			validateActions: func(t *testing.T, ctx *testinghelpers.FakeSyncContext, actions []clienttesting.Action) {
				testinghelpers.AssertNoActions(t, actions)
			},
		},
		{
			name:     "no addon leases",
			queueKey: "test/test",
			addOns: []runtime.Object{&addonv1alpha1.ManagedClusterAddOn{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testinghelpers.TestManagedClusterName,
					Name:      "test",
				},
				Spec: addonv1alpha1.ManagedClusterAddOnSpec{
					InstallNamespace: "test",
				},
			}},
			hubLeases: []runtime.Object{},
			leases:    []runtime.Object{},
			validateActions: func(t *testing.T, ctx *testinghelpers.FakeSyncContext, actions []clienttesting.Action) {
				testinghelpers.AssertActions(t, actions, "get", "update")
				actual := actions[1].(clienttesting.UpdateActionImpl).Object
				addOn := actual.(*addonv1alpha1.ManagedClusterAddOn)
				addOnCond := meta.FindStatusCondition(addOn.Status.Conditions, "Available")
				if addOnCond == nil {
					t.Errorf("expected addon available condition, but failed")
				}
				if addOnCond.Status != metav1.ConditionUnknown {
					t.Errorf("expected addon available condition is unknown, but failed")
				}
			},
		},
		{
			name:     "addon stop to update its lease",
			queueKey: "test/test",
			addOns: []runtime.Object{&addonv1alpha1.ManagedClusterAddOn{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testinghelpers.TestManagedClusterName,
					Name:      "test",
				},
				Spec: addonv1alpha1.ManagedClusterAddOnSpec{
					InstallNamespace: "test",
				},
			}},
			hubLeases: []runtime.Object{},
			leases: []runtime.Object{
				testinghelpers.NewAddOnLease("test", "test", now.Add(-5*time.Minute)),
			},
			validateActions: func(t *testing.T, ctx *testinghelpers.FakeSyncContext, actions []clienttesting.Action) {
				testinghelpers.AssertActions(t, actions, "get", "update")
				actual := actions[1].(clienttesting.UpdateActionImpl).Object
				addOn := actual.(*addonv1alpha1.ManagedClusterAddOn)
				addOnCond := meta.FindStatusCondition(addOn.Status.Conditions, "Available")
				if addOnCond == nil {
					t.Errorf("expected addon available condition, but failed")
				}
				if addOnCond.Status != metav1.ConditionFalse {
					t.Errorf("expected addon available condition is unavailable, but failed")
				}
			},
		},
		{
			name:     "addon update its lease constantly",
			queueKey: "test/test",
			addOns: []runtime.Object{&addonv1alpha1.ManagedClusterAddOn{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testinghelpers.TestManagedClusterName,
					Name:      "test",
				},
				Spec: addonv1alpha1.ManagedClusterAddOnSpec{
					InstallNamespace: "test",
				},
			}},
			hubLeases: []runtime.Object{},
			leases: []runtime.Object{
				testinghelpers.NewAddOnLease("test", "test", now),
			},
			validateActions: func(t *testing.T, ctx *testinghelpers.FakeSyncContext, actions []clienttesting.Action) {
				testinghelpers.AssertActions(t, actions, "get", "update")
				actual := actions[1].(clienttesting.UpdateActionImpl).Object
				addOn := actual.(*addonv1alpha1.ManagedClusterAddOn)
				addOnCond := meta.FindStatusCondition(addOn.Status.Conditions, "Available")
				if addOnCond == nil {
					t.Errorf("expected addon available condition, but failed")
				}
				if addOnCond.Status != metav1.ConditionTrue {
					t.Errorf("expected addon available condition is available, but failed")
				}
			},
		},
		{
			name:     "addon status is not changed",
			queueKey: "test/test",
			addOns: []runtime.Object{&addonv1alpha1.ManagedClusterAddOn{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testinghelpers.TestManagedClusterName,
					Name:      "test",
				},
				Spec: addonv1alpha1.ManagedClusterAddOnSpec{
					InstallNamespace: "test",
				},
				Status: addonv1alpha1.ManagedClusterAddOnStatus{
					Conditions: []metav1.Condition{
						{
							Type:    "Available",
							Status:  metav1.ConditionTrue,
							Reason:  "ManagedClusterAddOnLeaseUpdated",
							Message: "Managed cluster addon agent updates its lease constantly.",
						},
					},
				},
			}},
			hubLeases: []runtime.Object{},
			leases: []runtime.Object{
				testinghelpers.NewAddOnLease("test", "test", now),
			},
			validateActions: func(t *testing.T, ctx *testinghelpers.FakeSyncContext, actions []clienttesting.Action) {
				testinghelpers.AssertNoActions(t, actions)
			},
		},
		{
			name:     "sync all addons",
			queueKey: "key",
			addOns: []runtime.Object{
				&addonv1alpha1.ManagedClusterAddOn{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: testinghelpers.TestManagedClusterName,
						Name:      "test1",
					},
					Spec: addonv1alpha1.ManagedClusterAddOnSpec{
						InstallNamespace: "test",
					},
				},
				&addonv1alpha1.ManagedClusterAddOn{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: testinghelpers.TestManagedClusterName,
						Name:      "test2",
					},
				},
			},
			hubLeases: []runtime.Object{},
			leases: []runtime.Object{
				testinghelpers.NewAddOnLease("test1", "test1", now.Add(-5*time.Minute)),
			},
			validateActions: func(t *testing.T, ctx *testinghelpers.FakeSyncContext, actions []clienttesting.Action) {
				if ctx.Queue().Len() != 2 {
					t.Errorf("expected two addons in queue, but failed")
				}
			},
		},
		{
			name:     "addon update its lease constantly (compatibility)",
			queueKey: "test/test",
			addOns: []runtime.Object{&addonv1alpha1.ManagedClusterAddOn{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testinghelpers.TestManagedClusterName,
					Name:      "test",
				},
			}},
			hubLeases: []runtime.Object{testinghelpers.NewAddOnLease(testinghelpers.TestManagedClusterName, "test", now)},
			leases:    []runtime.Object{},
			validateActions: func(t *testing.T, ctx *testinghelpers.FakeSyncContext, actions []clienttesting.Action) {
				testinghelpers.AssertActions(t, actions, "get", "update")
				actual := actions[1].(clienttesting.UpdateActionImpl).Object
				addOn := actual.(*addonv1alpha1.ManagedClusterAddOn)
				addOnCond := meta.FindStatusCondition(addOn.Status.Conditions, "Available")
				if addOnCond == nil {
					t.Errorf("expected addon available condition, but failed")
				}
				if addOnCond.Status != metav1.ConditionTrue {
					t.Errorf("expected addon available condition is available, but failed")
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addOnClient := addonfake.NewSimpleClientset(c.addOns...)
			addOnInformerFactory := addoninformers.NewSharedInformerFactory(addOnClient, time.Minute*10)
			addOnStroe := addOnInformerFactory.Addon().V1alpha1().ManagedClusterAddOns().Informer().GetStore()
			for _, addOn := range c.addOns {
				addOnStroe.Add(addOn)
			}

			hubClient := kubefake.NewSimpleClientset(c.hubLeases...)

			leaseClient := kubefake.NewSimpleClientset(c.leases...)

			ctrl := &managedClusterAddOnLeaseController{
				clusterName:    testinghelpers.TestManagedClusterName,
				clock:          clock.NewFakeClock(time.Now()),
				hubLeaseClient: hubClient.CoordinationV1(),
				addOnClient:    addOnClient,
				addOnLister:    addOnInformerFactory.Addon().V1alpha1().ManagedClusterAddOns().Lister(),
				leaseClient:    leaseClient.CoordinationV1(),
			}
			syncCtx := testinghelpers.NewFakeSyncContext(t, c.queueKey)
			syncErr := ctrl.sync(context.TODO(), syncCtx)
			if syncErr != nil {
				t.Errorf("unexpected err: %v", syncErr)
			}

			c.validateActions(t, syncCtx, addOnClient.Actions())
		})
	}
}
