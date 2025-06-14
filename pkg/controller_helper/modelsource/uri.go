/*
Copyright 2024 The InftyAI Team.

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

package modelSource

import (
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"github.com/inftyai/llmaz/pkg"
)

var _ ModelSourceProvider = &URIProvider{}

const (
	GCS      = "GCS"
	OSS      = "OSS"
	S3       = "S3"
	Ollama   = "OLLAMA"
	HostPath = "HOST"
)

type URIProvider struct {
	modelName string
	protocol  string
	bucket    string
	endpoint  string
	modelPath string
	uri       string
}

func (p *URIProvider) ModelName() string {
	if p.protocol == Ollama {
		// model path stores the ollama model name,
		// the model name is the name of model CRD.
		return p.modelPath
	}
	return p.modelName
}

// Example 1:
//   - uri: bucket.endpoint/modelPath/opt-125m
//     modelPath: /workspace/models/models--opt-125m
//
// Example 2:
//   - uri: bucket.endpoint/modelPath/model.gguf
//     modelPath: /workspace/models/model.gguf
func (p *URIProvider) ModelPath(skipModelLoader bool) string {
	if p.protocol == HostPath {
		return p.modelPath
	}

	// Skip the model loader to allow the inference engine to handle loading models directly from remote storage (e.g., S3, OSS).
	// In this case, the remote model path should be returned (e.g., s3://bucket/modelPath).
	if skipModelLoader {
		return p.uri
	}

	// protocol is oss.
	splits := strings.Split(p.modelPath, "/")

	if strings.Contains(p.modelPath, ".gguf") {
		return CONTAINER_MODEL_PATH + splits[len(splits)-1]
	}
	return CONTAINER_MODEL_PATH + "models--" + splits[len(splits)-1]
}

func (p *URIProvider) InjectModelLoader(template *corev1.PodTemplateSpec, index int) {
	// We don't have additional operations for Ollama, just load in runtime.
	if p.protocol == Ollama {
		return
	}

	if p.protocol == HostPath {
		template.Spec.Volumes = append(template.Spec.Volumes, corev1.Volume{
			Name: MODEL_VOLUME_NAME,
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: p.modelPath,
				},
			},
		})

		for i, container := range template.Spec.Containers {
			// We only consider this container.
			if container.Name == MODEL_RUNNER_CONTAINER_NAME {
				template.Spec.Containers[i].VolumeMounts = append(template.Spec.Containers[i].VolumeMounts, corev1.VolumeMount{
					Name:      MODEL_VOLUME_NAME,
					MountPath: p.modelPath,
					ReadOnly:  true,
				})
			}
		}
		return
	}

	// Other protocols.
	initContainerName := MODEL_LOADER_CONTAINER_NAME
	if index != 0 {
		initContainerName += "-" + strconv.Itoa(index)
	}
	// Handle initContainer.
	initContainer := &corev1.Container{
		Name:  initContainerName,
		Image: pkg.LOADER_IMAGE,
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      MODEL_VOLUME_NAME,
				MountPath: CONTAINER_MODEL_PATH,
			},
		},
	}

	// We have exactly one container in the template.Spec.Containers.
	spreadEnvToInitContainer(template.Spec.Containers[0].Env, initContainer)

	switch p.protocol {
	case OSS:
		initContainer.Env = append(
			initContainer.Env,
			corev1.EnvVar{Name: "MODEL_SOURCE_TYPE", Value: MODEL_SOURCE_MODEL_OBJ_STORE},
			corev1.EnvVar{Name: "PROVIDER", Value: OSS},
			corev1.EnvVar{Name: "ENDPOINT", Value: p.endpoint},
			corev1.EnvVar{Name: "BUCKET", Value: p.bucket},
			corev1.EnvVar{Name: "MODEL_PATH", Value: p.modelPath},
			corev1.EnvVar{
				Name: OSS_ACCESS_KEY_ID,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: OSS_ACCESS_SECRET_NAME, // if secret not exists, the env is empty.
						},
						Key:      OSS_ACCESS_KEY_ID,
						Optional: ptr.To[bool](true),
					},
				},
			},
			corev1.EnvVar{
				Name: OSS_ACCESS_KEY_SECRET,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: OSS_ACCESS_SECRET_NAME, // if secret not exists, the env is empty.
						},
						Key:      OSS_ACCESS_KEY_SECRET,
						Optional: ptr.To[bool](true),
					},
				},
			},
		)
	}

	template.Spec.InitContainers = append(template.Spec.InitContainers, *initContainer)
}

func (p *URIProvider) InjectModelEnvVars(template *corev1.PodTemplateSpec) {
	switch p.protocol {
	case S3, GCS:
		for i := range template.Spec.Containers {
			if template.Spec.Containers[i].Name == MODEL_RUNNER_CONTAINER_NAME {
				// Check if AWS credentials already exist
				awsKeyIDExists := false
				awsKeySecretExists := false
				for _, env := range template.Spec.Containers[i].Env {
					if env.Name == AWS_ACCESS_KEY_ID {
						awsKeyIDExists = true
					}
					if env.Name == AWS_ACCESS_KEY_SECRET {
						awsKeySecretExists = true
					}
				}

				// Add AWS_ACCESS_KEY_ID if it doesn't exist
				if !awsKeyIDExists {
					template.Spec.Containers[i].Env = append(template.Spec.Containers[i].Env,
						corev1.EnvVar{
							Name: AWS_ACCESS_KEY_ID,
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: AWS_ACCESS_SECRET_NAME, // if secret not exists, the env is empty.
									},
									Key:      AWS_ACCESS_KEY_ID,
									Optional: ptr.To[bool](true),
								},
							},
						})
				}

				// Add AWS_ACCESS_KEY_SECRET if it doesn't exist
				if !awsKeySecretExists {
					template.Spec.Containers[i].Env = append(template.Spec.Containers[i].Env,
						corev1.EnvVar{
							Name: AWS_ACCESS_KEY_SECRET,
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: AWS_ACCESS_SECRET_NAME, // if secret not exists, the env is empty.
									},
									Key:      AWS_ACCESS_KEY_SECRET,
									Optional: ptr.To[bool](true),
								},
							},
						})
				}
			}
		}
	case OSS:
		for i := range template.Spec.Containers {
			if template.Spec.Containers[i].Name == MODEL_RUNNER_CONTAINER_NAME {
				// Check if OSS credentials already exist
				ossKeyIDExists := false
				ossKeySecretExists := false
				for _, env := range template.Spec.Containers[i].Env {
					if env.Name == OSS_ACCESS_KEY_ID {
						ossKeyIDExists = true
					}
					if env.Name == OSS_ACCESS_KEY_SECRET {
						ossKeySecretExists = true
					}
				}

				// Add OSS_ACCESS_KEY_ID if it doesn't exist
				if !ossKeyIDExists {
					template.Spec.Containers[i].Env = append(template.Spec.Containers[i].Env,
						corev1.EnvVar{
							Name: OSS_ACCESS_KEY_ID,
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: OSS_ACCESS_SECRET_NAME, // if secret not exists, the env is empty.
									},
									Key:      OSS_ACCESS_KEY_ID,
									Optional: ptr.To[bool](true),
								},
							},
						})
				}

				// Add OSS_ACCESS_KEY_SECRET if it doesn't exist
				if !ossKeySecretExists {
					template.Spec.Containers[i].Env = append(template.Spec.Containers[i].Env,
						corev1.EnvVar{
							Name: OSS_ACCESS_KEY_SECRET,
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: OSS_ACCESS_SECRET_NAME, // if secret not exists, the env is empty.
									},
									Key:      OSS_ACCESS_KEY_SECRET,
									Optional: ptr.To[bool](true),
								},
							},
						})
				}
			}
		}
	}
}
