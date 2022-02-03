package general

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/alibaba/kt-connect/pkg/common"
	"github.com/alibaba/kt-connect/pkg/kt"
	"github.com/alibaba/kt-connect/pkg/kt/cluster"
	"github.com/alibaba/kt-connect/pkg/kt/dns"
	opt "github.com/alibaba/kt-connect/pkg/kt/options"
	"github.com/alibaba/kt-connect/pkg/kt/util"
	"github.com/rs/zerolog/log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// CleanupWorkspace clean workspace
func CleanupWorkspace(cli kt.CliInterface) {
	log.Info().Msgf("Cleaning workspace")
	cleanLocalFiles()
	if opt.Get().RuntimeOptions.Component == common.ComponentConnect {
		recoverGlobalHostsAndProxy()
	}

	ctx := context.Background()
	k8s := cli.Kubernetes()
	if opt.Get().RuntimeOptions.Component == common.ComponentExchange {
		recoverExchangedTarget(ctx, k8s)
	} else if opt.Get().RuntimeOptions.Component == common.ComponentMesh {
		recoverAutoMeshRoute(ctx, k8s)
	}
	cleanService(ctx, k8s)
	cleanShadowPodAndConfigMap(ctx, k8s)
}

func recoverGlobalHostsAndProxy() {
	if strings.HasPrefix(opt.Get().ConnectOptions.DnsMode, common.DnsModeHosts) || opt.Get().ConnectOptions.DnsMode == common.DnsModeLocalDns {
		log.Debug().Msg("Dropping hosts records ...")
		dns.DropHosts()
	}
}

func cleanLocalFiles() {
	if opt.Get().RuntimeOptions.Component == "" {
		return
	}
	pidFile := fmt.Sprintf("%s/%s-%d.pid", common.KtHome, opt.Get().RuntimeOptions.Component, os.Getpid())
	if err := os.Remove(pidFile); os.IsNotExist(err) {
		log.Debug().Msgf("Pid file %s not exist", pidFile)
	} else if err != nil {
		log.Debug().Err(err).Msgf("Remove pid file %s failed", pidFile)
	} else {
		log.Info().Msgf("Removed pid file %s", pidFile)
	}

	if opt.Get().RuntimeOptions.Shadow != "" {
		for _, sshcm := range strings.Split(opt.Get().RuntimeOptions.Shadow, ",") {
			file := util.PrivateKeyPath(sshcm)
			if err := os.Remove(file); os.IsNotExist(err) {
				log.Debug().Msgf("Key file %s not exist", file)
			} else if err != nil {
				log.Debug().Msgf("Remove key file %s failed", pidFile)
			} else {
				log.Info().Msgf("Removed key file %s", file)
			}
		}
	}
}

func recoverExchangedTarget(ctx context.Context, k cluster.KubernetesInterface) {
	if opt.Get().RuntimeOptions.Origin == "" {
		// process exit before target exchanged
		return
	}
	if opt.Get().ExchangeOptions.Mode == common.ExchangeModeScale {
		log.Info().Msgf("Recovering origin deployment %s", opt.Get().RuntimeOptions.Origin)
		err := k.ScaleTo(ctx, opt.Get().RuntimeOptions.Origin, opt.Get().Namespace, &opt.Get().RuntimeOptions.Replicas)
		if err != nil {
			log.Error().Err(err).Msgf("Scale deployment %s to %d failed",
				opt.Get().RuntimeOptions.Origin, opt.Get().RuntimeOptions.Replicas)
		}
		// wait for scale complete
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		go func() {
			waitDeploymentRecoverComplete(ctx, k)
			ch <- os.Interrupt
		}()
		_ = <-ch
	} else if opt.Get().ExchangeOptions.Mode == common.ExchangeModeSelector {
		RecoverOriginalService(ctx, k, opt.Get().RuntimeOptions.Origin, opt.Get().Namespace)
	}
}

func recoverAutoMeshRoute(ctx context.Context, k cluster.KubernetesInterface) {
	if opt.Get().RuntimeOptions.Router != "" {
		routerPod, err := k.GetPod(ctx, opt.Get().RuntimeOptions.Router, opt.Get().Namespace)
		if err != nil {
			log.Error().Err(err).Msgf("Router pod has been removed unexpectedly")
			return
		}
		if shouldDelRouter, err2 := k.DecreaseRef(ctx, opt.Get().RuntimeOptions.Router, opt.Get().Namespace); err2 != nil {
			log.Error().Err(err2).Msgf("Decrease router pod %s reference failed", opt.Get().RuntimeOptions.Shadow)
		} else if shouldDelRouter {
			recoverService(ctx, k, routerPod.Annotations[common.KtConfig])
		} else {
			stdout, stderr, err3 := k.ExecInPod(common.DefaultContainer, opt.Get().RuntimeOptions.Router, opt.Get().Namespace,
				common.RouterBin, "remove", opt.Get().RuntimeOptions.Mesh)
			log.Debug().Msgf("Stdout: %s", stdout)
			log.Debug().Msgf("Stderr: %s", stderr)
			if err3 != nil {
				log.Error().Err(err3).Msgf("Failed to remove version %s from router pod", opt.Get().RuntimeOptions.Mesh)
			}
		}
	}
}

func recoverService(ctx context.Context, k cluster.KubernetesInterface, routerConfig string) {
	config := util.String2Map(routerConfig)
	svcName := config["service"]
	RecoverOriginalService(ctx, k, svcName, opt.Get().Namespace)

	originSvcName := svcName + common.OriginServiceSuffix
	if err := k.RemoveService(ctx, originSvcName, opt.Get().Namespace); err != nil {
		log.Error().Err(err).Msgf("Failed to remove origin service %s", originSvcName)
	}
	log.Info().Msgf("Substitution service %s removed", originSvcName)
}

func RecoverOriginalService(ctx context.Context, k cluster.KubernetesInterface, svcName, namespace string) {
	if svc, err := k.GetService(ctx, svcName, namespace); err != nil {
		log.Error().Err(err).Msgf("Original service %s not found", svcName)
		return
	} else {
		var selector map[string]string
		if svc.Annotations == nil {
			log.Warn().Msgf("No annotation found in service %s, skipping", svcName)
			return
		}
		originSelector, ok := svc.Annotations[common.KtSelector]
		if !ok {
			log.Warn().Msgf("No selector annotation found in service %s, skipping", svcName)
			return
		}
		err = json.Unmarshal([]byte(originSelector), &selector)
		if err != nil {
			log.Error().Err(err).Msgf("Failed to unmarshal original selector of service %s", svcName)
			return
		}
		svc.Spec.Selector = selector
		delete(svc.Annotations, common.KtSelector)
		if _, err = k.UpdateService(ctx, svc); err != nil {
			log.Error().Err(err).Msgf("Failed to recover selector of original service %s", svcName)
		}
	}
	log.Info().Msgf("Original service %s recovered", svcName)
}

func waitDeploymentRecoverComplete(ctx context.Context, k cluster.KubernetesInterface) {
	ok := false
	counts := opt.Get().ExchangeOptions.RecoverWaitTime / 5
	for i := 0; i < counts; i++ {
		deployment, err := k.GetDeployment(ctx, opt.Get().RuntimeOptions.Origin, opt.Get().Namespace)
		if err != nil {
			log.Error().Err(err).Msgf("Cannot fetch original deployment %s", opt.Get().RuntimeOptions.Origin)
			break
		} else if deployment.Status.ReadyReplicas == opt.Get().RuntimeOptions.Replicas {
			ok = true
			break
		} else {
			log.Info().Msgf("Wait for deployment %s recover ...", opt.Get().RuntimeOptions.Origin)
			time.Sleep(5 * time.Second)
		}
	}
	if !ok {
		log.Warn().Msgf("Deployment %s recover timeout", opt.Get().RuntimeOptions.Origin)
	}
}

func cleanService(ctx context.Context, k cluster.KubernetesInterface) {
	if opt.Get().RuntimeOptions.Service != "" {
		log.Info().Msgf("Cleaning service %s", opt.Get().RuntimeOptions.Service)
		err := k.RemoveService(ctx, opt.Get().RuntimeOptions.Service, opt.Get().Namespace)
		if err != nil {
			log.Error().Err(err).Msgf("Delete service %s failed", opt.Get().RuntimeOptions.Service)
		}
	}
}

func cleanShadowPodAndConfigMap(ctx context.Context, k cluster.KubernetesInterface) {
	var err error
	if opt.Get().RuntimeOptions.Shadow != "" {
		shouldDelWithShared := false
		if opt.Get().ConnectOptions.SharedShadow {
			shouldDelWithShared, err = k.DecreaseRef(ctx, opt.Get().RuntimeOptions.Shadow, opt.Get().Namespace)
			if err != nil {
				log.Error().Err(err).Msgf("Decrease shadow daemon pod %s ref count failed", opt.Get().RuntimeOptions.Shadow)
			}
		}
		if shouldDelWithShared || !opt.Get().ConnectOptions.SharedShadow {
			for _, sshcm := range strings.Split(opt.Get().RuntimeOptions.Shadow, ",") {
				log.Info().Msgf("Cleaning configmap %s", sshcm)
				err = k.RemoveConfigMap(ctx, sshcm, opt.Get().Namespace)
				if err != nil {
					log.Error().Err(err).Msgf("Delete configmap %s failed", sshcm)
				}
			}
		}
		if opt.Get().ExchangeOptions.Mode == common.ExchangeModeEphemeral {
			for _, shadow := range strings.Split(opt.Get().RuntimeOptions.Shadow, ",") {
				log.Info().Msgf("Removing ephemeral container of pod %s", shadow)
				err = k.RemoveEphemeralContainer(ctx, common.KtExchangeContainer, shadow, opt.Get().Namespace)
				if err != nil {
					log.Error().Err(err).Msgf("Remove ephemeral container of pod %s failed", shadow)
				}
			}
		} else {
			for _, shadow := range strings.Split(opt.Get().RuntimeOptions.Shadow, ",") {
				log.Info().Msgf("Cleaning shadow pod %s", shadow)
				err = k.RemovePod(ctx, shadow, opt.Get().Namespace)
				if err != nil {
					log.Error().Err(err).Msgf("Delete shadow pod %s failed", shadow)
				}
			}
		}
	}
}
