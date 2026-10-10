// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"path"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	k8sptr "k8s.io/utils/ptr"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/redisconfig"
)

const (
	// SessionRedisTLSCACertVolumeName is the pod volume holding the CA bundle
	// used to verify the Redis session storage server certificate.
	SessionRedisTLSCACertVolumeName = "session-redis-tls-ca"

	// SessionRedisTLSCACertMountPath is the directory where the session storage
	// Redis CA bundle is mounted. It is distinct from the embedded auth server's
	// RedisTLSCACertMountPath so both can be mounted in the same container.
	SessionRedisTLSCACertMountPath = "/etc/toolhive/session-redis-tls"

	// SessionRedisTLSCACertFileName is the file name of the mounted CA bundle.
	SessionRedisTLSCACertFileName = "ca.crt"
)

// SessionRedisTLSConfig returns the runtime TLS settings for Redis session
// storage, or nil when session storage is not Redis or TLS is not configured
// (plaintext). When a CA Secret is referenced, CACertFile points at the file
// mounted by SessionRedisTLSVolumes.
func SessionRedisTLSConfig(ss *mcpv1beta1.SessionStorageConfig) *redisconfig.TLSConfig {
	tlsCfg := sessionRedisTLS(ss)
	if tlsCfg == nil {
		return nil
	}
	rc := &redisconfig.TLSConfig{InsecureSkipVerify: tlsCfg.InsecureSkipVerify}
	if tlsCfg.CACertSecretRef != nil {
		rc.CACertFile = path.Join(SessionRedisTLSCACertMountPath, SessionRedisTLSCACertFileName)
	}
	return rc
}

// SessionRedisTLSVolumes returns the volume and mount that project the
// session storage Redis CA Secret into the container, or nil slices when no
// CA Secret is referenced.
func SessionRedisTLSVolumes(ss *mcpv1beta1.SessionStorageConfig) ([]corev1.Volume, []corev1.VolumeMount) {
	tlsCfg := sessionRedisTLS(ss)
	if tlsCfg == nil || tlsCfg.CACertSecretRef == nil {
		return nil, nil
	}
	vol, mount := secretFileVolume(SessionRedisTLSCACertVolumeName, tlsCfg.CACertSecretRef,
		SessionRedisTLSCACertMountPath, SessionRedisTLSCACertFileName)
	return []corev1.Volume{vol}, []corev1.VolumeMount{mount}
}

// SessionRedisTLSVolumeNeedsUpdate reports whether the live pod volumes project
// a different session storage CA Secret than the desired pod volumes. The
// RunConfig checksum already triggers a rollout when TLS is enabled or
// disabled, but the mounted file path is fixed, so a change to the referenced
// Secret name or key is only visible in the volume itself. Callers pass the
// desired volumes after any pod template patch has been applied, so a patch
// that overrides this volume is not reported as drift.
func SessionRedisTLSVolumeNeedsUpdate(live, desired []corev1.Volume) bool {
	got := findVolume(live, SessionRedisTLSCACertVolumeName)
	want := findVolume(desired, SessionRedisTLSCACertVolumeName)
	if want == nil || got == nil {
		return (want == nil) != (got == nil)
	}
	if (want.Secret == nil) != (got.Secret == nil) {
		return true
	}
	if want.Secret == nil {
		return false
	}
	return got.Secret.SecretName != want.Secret.SecretName ||
		!equality.Semantic.DeepEqual(got.Secret.Items, want.Secret.Items)
}

// sessionRedisTLS returns the TLS block of a Redis session storage config, or
// nil when session storage is not Redis or TLS is not configured.
func sessionRedisTLS(ss *mcpv1beta1.SessionStorageConfig) *mcpv1beta1.RedisTLSConfig {
	if ss == nil || ss.Provider != mcpv1beta1.SessionStorageProviderRedis {
		return nil
	}
	return ss.TLS
}

func findVolume(volumes []corev1.Volume, name string) *corev1.Volume {
	for i := range volumes {
		if volumes[i].Name == name {
			return &volumes[i]
		}
	}
	return nil
}

// secretFileVolume projects a single key of a Secret as an owner-read-only file
// at mountDir/fileName, mounted with subPath so other files in mountDir are
// left untouched.
func secretFileVolume(
	volumeName string, ref *mcpv1beta1.SecretKeyRef, mountDir, fileName string,
) (corev1.Volume, corev1.VolumeMount) {
	return corev1.Volume{
		Name: volumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName:  ref.Name,
				Items:       []corev1.KeyToPath{{Key: ref.Key, Path: fileName}},
				DefaultMode: k8sptr.To(int32(0400)),
			},
		},
	}, corev1.VolumeMount{
		Name:      volumeName,
		MountPath: path.Join(mountDir, fileName),
		SubPath:   fileName,
		ReadOnly:  true,
	}
}
