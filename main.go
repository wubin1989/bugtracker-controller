package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/unionj-cloud/go-doudou/v2/toolkit/dotenv"
	"github.com/unionj-cloud/go-doudou/v2/toolkit/stringutils"
	"github.com/unionj-cloud/go-doudou/v2/toolkit/zlogger"
	"io/ioutil"
	appsv1 "k8s.io/api/apps/v1"
	"net/http"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"
)

func init() {
	// 加载.env配置文件
	dotenv.Load("")
}

var (
	// 参考https://ambassadorlabs.github.io/k8s-for-humans/
	bugTrackerAnnotation = "a8r.io/bugs"
)

type Controller struct {
	indexer  cache.Indexer
	queue    workqueue.RateLimitingInterface
	informer cache.Controller
}

func NewController(queue workqueue.RateLimitingInterface, indexer cache.Indexer, informer cache.Controller) *Controller {
	return &Controller{
		informer: informer,
		indexer:  indexer,
		queue:    queue,
	}
}

func (c *Controller) processNextItem() bool {
	// 如果队列为空，会一直阻塞，直到队列中出现key
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	// 告诉队列这个key已经处理完毕，不再处理了
	defer c.queue.Done(key)

	// 调用Reconcile方法，实现自定义的业务逻辑
	err := c.Reconcile(key.(string))
	// 处理error
	c.handleErr(err, key)
	return true
}

// Reconcile 实现具体的业务逻辑。本示例中的逻辑是：从deployment对象中查找bugTrackerAnnotation注解，如果不存在，则直接返回nil，
// 如果存在，则表示本次部署修复了bugTrackerAnnotation注解值所表示的缺陷，并通过钉钉群机器人通知相关验收人员。
func (c *Controller) Reconcile(key string) error {
	obj, exists, err := c.indexer.GetByKey(key)
	if err != nil {
		zlogger.Error().Err(err).Msgf("Fetching object with key %s from store failed with %v", key, err)
		return err
	}

	if !exists {
		// 此处直接忽略值不存在的情况
		return nil
	}

	deployment := obj.(*appsv1.Deployment)
	bug := deployment.Annotations[bugTrackerAnnotation]
	if stringutils.IsEmpty(bug) {
		return nil
	}
	msg := fmt.Sprintf("缺陷修复消息：缺陷%s已修复\n", bug)
	zlogger.Info().Msg(msg)

	c.notify(msg)
	return nil
}

// notify 具体执行钉钉通知逻辑
func (c *Controller) notify(msg string) {
	webHook := os.Getenv("DINGTALK_WEBHOOK")
	if stringutils.IsEmpty(webHook) {
		zlogger.Error().Msg("environment variable DINGTALK_WEBHOOK not set")
	}
	content := `{"msgtype": "text",
			"text": {"content": "` + msg + `"}
		}`
	//创建一个请求
	req, err := http.NewRequest("POST", webHook, strings.NewReader(content))
	if err != nil {
		// handle error
		zlogger.Error().Err(err).Msg("send dingtalk notify message failed")
	}

	client := &http.Client{}
	//设置请求头
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	//发送请求
	resp, err := client.Do(req)
	//关闭请求
	defer resp.Body.Close()

	if err != nil {
		zlogger.Error().Err(err).Msg("send dingtalk notify message failed")
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		zlogger.Error().Err(err).Msg("send dingtalk notify message failed")
	}

	result := make(map[string]interface{})
	if err = json.Unmarshal(body, &result); err != nil {
		zlogger.Error().Err(err).Msg("send dingtalk notify message failed")
	}

	if result["errcode"].(float64) != 0 {
		zlogger.Error().Float64("errcode", result["errcode"].(float64)).Msg("send dingtalk notify message failed")
	}
}

// handleErr 判断是否存在错误，并确保后续还有机会重试
func (c *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		// 每次成功完成Reconcile后重置rateLimiter
		c.queue.Forget(key)
		return
	}

	// 同一个key在一轮rateLimiter中最多可以入队5次，即被处理5次
	if c.queue.NumRequeues(key) < 5 {
		zlogger.Info().Msgf("Error syncing pod %v: %v", key, err)

		// 该key再次入队
		c.queue.AddRateLimited(key)
		return
	}

	c.queue.Forget(key)
	// 向其他错误监听器报告错误
	runtime.HandleError(err)
	zlogger.Info().Msgf("Dropping pod %q out of the queue: %v", key, err)
}

func (c *Controller) Run(threadiness int, stopCh chan struct{}) {
	defer runtime.HandleCrash()

	// 停止worker的工作，释放资源
	defer c.queue.ShutDown()
	zlogger.Info().Msg("Starting Pod controller")

	go c.informer.Run(stopCh)

	// 等待缓存初始化完毕：拉取deployments对象列表，并全部加入自定义控制器c的queue队列
	if !cache.WaitForCacheSync(stopCh, c.informer.HasSynced) {
		runtime.HandleError(fmt.Errorf("Timed out waiting for caches to sync"))
		return
	}

	for i := 0; i < threadiness; i++ {
		// 开启worker协程
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	<-stopCh
	zlogger.Info().Msg("Stopping Pod controller")
}

func (c *Controller) runWorker() {
	for c.processNextItem() {
	}
}

func main() {
	var kubeconfig string
	var master string

	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	flag.StringVar(&master, "master", "", "master url")
	flag.Parse()

	// 创建REST客户端配置对象
	config, err := clientcmd.BuildConfigFromFlags(master, kubeconfig)
	if err != nil {
		zlogger.Fatal().Err(err).Msg("failed to create restclient.Config")
	}

	// 创建客户端集合clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		zlogger.Fatal().Err(err).Msg("failed to create kubernetes.Clientset")
	}

	// 创建deployment对象的ListWatcher
	deployListWatcher := cache.NewListWatchFromClient(clientset.AppsV1().RESTClient(), "deployments", corev1.NamespaceDefault, fields.Everything())

	// 创建工作队列workqueue
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	// 调用以下方法的作用是将上面创建的工作队列queue通过informer通知器与缓存相绑定。这样做可以确保当缓存有更新，deployment对象的key可以被加入工作队列。
	// 注意我们要在UpdateFunc钩子函数中判断一下新旧两个对象的Generation的值，因为Generation值相同的更新事件会触发两次。如果不做判断并过滤掉一次，我们实际上会对同一次更新做两次处理。
	indexer, informer := cache.NewIndexerInformer(deployListWatcher, &appsv1.Deployment{}, 0, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(obj)
			if err == nil {
				queue.Add(key)
			}
		},
		UpdateFunc: func(old interface{}, new interface{}) {
			oldObj := old.(*appsv1.Deployment)
			newObj := new.(*appsv1.Deployment)
			if newObj.GetGeneration() == oldObj.GetGeneration() {
				return
			}
			key, err := cache.MetaNamespaceKeyFunc(new)
			if err == nil {
				queue.Add(key)
			}
		},
		DeleteFunc: func(obj interface{}) {
			// Informer通知器使用增量队列DeltaFIFO，所以我们这里要调用DeletionHandlingMetaNamespaceKeyFunc方法
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err == nil {
				queue.Add(key)
			}
		},
	}, cache.Indexers{})

	controller := NewController(queue, indexer, informer)

	// 启动自定义控制器
	stop := make(chan struct{})
	defer close(stop)
	go controller.Run(1, stop)

	// 永久等待
	select {}
}
