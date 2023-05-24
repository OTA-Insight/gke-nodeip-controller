package controllers

import (
	"context"
	mock_ipmanager "nodeip-controller/mocks"
	"nodeip-controller/pkg/ipmanager"
	"time"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

var _ = Describe("NodeIp controller", func() {
	// ctx := context.Background()
	var (
		mockIpManager *mock_ipmanager.MockIpManager
		mockCtrl      *gomock.Controller
		ctx           context.Context
		cancel        context.CancelFunc
	)
	BeforeEach(func() {
		// test runs don't clean up.
		k8sClient.DeleteAllOf(context.TODO(), &corev1.Node{})
		mockCtrl = gomock.NewController(GinkgoT())
		mockIpManager = mock_ipmanager.NewMockIpManager(mockCtrl)
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{})
		ctx, cancel = context.WithCancel(context.TODO())
		Expect(err).NotTo(HaveOccurred(), "failed to create manager")
		nodePoolIpManagers := map[string]ipmanager.IpManager{"v1": mockIpManager}
		controller := &NodeReconciler{
			Client:             mgr.GetClient(),
			Scheme:             mgr.GetScheme(),
			NodePoolIpManagers: nodePoolIpManagers,
		}
		err = controller.SetupWithManager(mgr)
		Expect(err).NotTo(HaveOccurred(), "failed to setup controller")

		go func() {
			defer GinkgoRecover()
			err := mgr.Start(ctx)
			Expect(err).NotTo(HaveOccurred(), "failed to start manager")
		}()
	})
	AfterEach(func() {
		cancel()
		mockCtrl.Finish()
	})
	// SetupTestMocks(ctx, )
	// Define utility constants for object names and testing timeouts/durations and intervals.
	const (
		NodeName = "nodename"

		duration = time.Second * 15
		interval = time.Millisecond * 250
	)

	Context("Different nodepool", func() {
		It("Does nothing", func() {
			key := types.NamespacedName{Name: NodeName}

			node := &corev1.Node{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Node",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:   key.Name,
					Labels: map[string]string{nodePoolLabel: "differentvalue", "topology.kubernetes.io/zone": "zone-1"},
				},
				Spec: corev1.NodeSpec{},
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{{
						Type:    corev1.NodeInternalIP,
						Address: "10.10.10.1",
					}, {
						Type:    corev1.NodeExternalIP,
						Address: "127.0.0.1",
					}},
				},
			}
			Expect(k8sClient.Create(ctx, node)).Should(Succeed())
			createdNode := &corev1.Node{}
			mockIpManager.EXPECT().IsAllowedIpAddress(gomock.Any()).Return(false).Times(0)
			Consistently(func(g Gomega) {
				err := k8sClient.Get(ctx, key, createdNode)
				g.Expect(err).NotTo(HaveOccurred())
			}, duration, interval).Should(Succeed())
		})
	})
	Context("When a node has a correct IP address", func() {
		It("Does nothing", func() {
			key := types.NamespacedName{Name: NodeName}

			node := &corev1.Node{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Node",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:   key.Name,
					Labels: map[string]string{nodePoolLabel: "v1", "topology.kubernetes.io/zone": "zone-1"},
				},
				Spec: corev1.NodeSpec{},
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{{
						Type:    corev1.NodeInternalIP,
						Address: "10.10.10.2",
					}, {
						Type:    corev1.NodeExternalIP,
						Address: "127.0.0.2",
					}},
				},
			}
			Expect(k8sClient.Create(ctx, node)).Should(Succeed())
			createdNode := &corev1.Node{}
			mockIp := ipmanager.IpAddress{}
			mockIpManager.EXPECT().IsAllowedIpAddress("127.0.0.2").Return(true).Times(1)
			mockIpManager.EXPECT().AssignAllowedIpAddress(gomock.Any(), key.Name, "zone-1").Return(mockIp, nil).Times(0)
			Consistently(func(g Gomega) {
				err := k8sClient.Get(ctx, key, createdNode)
				g.Expect(err).NotTo(HaveOccurred())
			}, duration, interval).Should(Succeed())
		})
	})
	Context("When a node has an incorrect IP address", func() {
		It("Assigns a correct IP", func() {
			key := types.NamespacedName{Name: NodeName}

			node := &corev1.Node{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Node",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:   key.Name,
					Labels: map[string]string{nodePoolLabel: "v1", "topology.kubernetes.io/zone": "zone-1"},
				},
				Spec: corev1.NodeSpec{},
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{{
						Type:    corev1.NodeInternalIP,
						Address: "10.10.10.3",
					}, {
						Type:    corev1.NodeExternalIP,
						Address: "127.0.0.3",
					}},
				},
			}
			Expect(k8sClient.Create(ctx, node)).Should(Succeed())
			createdNode := &corev1.Node{}
			mockIp := ipmanager.IpAddress{Name: "mock", Address: "127.0.0.254", Assigned: true}
			mockIpManager.EXPECT().IsAllowedIpAddress("127.0.0.3").Return(false).Times(1)
			mockIpManager.EXPECT().AssignAllowedIpAddress(gomock.Any(), key.Name, "zone-1").Return(mockIp, nil).Times(1)
			Consistently(func(g Gomega) {
				err := k8sClient.Get(ctx, key, createdNode)
				g.Expect(err).NotTo(HaveOccurred())
			}, duration, interval).Should(Succeed())
		})
	})
	Context("When a node has no external IP address", func() {
		It("Assigns a correct IP address", func() {
			key := types.NamespacedName{Name: NodeName}

			node := &corev1.Node{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Node",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:   key.Name,
					Labels: map[string]string{nodePoolLabel: "v1", "topology.kubernetes.io/zone": "zone-1"},
				},
				Spec: corev1.NodeSpec{},
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{{
						Type:    corev1.NodeInternalIP,
						Address: "10.10.10.4",
					}},
				},
			}
			Expect(k8sClient.Create(ctx, node)).Should(Succeed())
			createdNode := &corev1.Node{}
			mockIp := ipmanager.IpAddress{Name: "mock", Address: "127.0.0.254", Assigned: true}
			mockIpManager.EXPECT().IsAllowedIpAddress("127.0.0.4").Return(true).Times(0)
			mockIpManager.EXPECT().AssignAllowedIpAddress(gomock.Any(), key.Name, "zone-1").Return(mockIp, nil).Times(1)
			Consistently(func(g Gomega) {
				err := k8sClient.Get(ctx, key, createdNode)
				g.Expect(err).NotTo(HaveOccurred())
			}, duration, interval).Should(Succeed())
		})
	})
})
