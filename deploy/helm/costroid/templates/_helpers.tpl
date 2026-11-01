{{- define "costroid.name" -}}
{{- printf "%s-costroid" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "costroid.postgresName" -}}
{{- printf "%s-postgres" (include "costroid.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "costroid.secretName" -}}
{{- .Values.existingSecret | default (include "costroid.name" .) -}}
{{- end -}}

{{- define "costroid.labels" -}}
app.kubernetes.io/name: costroid
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
{{- end -}}

{{- define "costroid.selector" -}}
app.kubernetes.io/name: costroid
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "costroid.image" -}}
{{- if .Values.image.digest -}}
{{ .Values.image.repository }}@{{ .Values.image.digest }}
{{- else -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}
{{- end -}}

{{- define "costroid.check" -}}
{{- if and (not .Values.postgres.enabled) (not .Values.existingSecret) -}}
{{- fail "postgres.enabled is false, so existingSecret must name the Secret with your Postgres connections" -}}
{{- end -}}
{{- if and .Values.postgres.enabled .Values.existingSecret -}}
{{- fail "existingSecret names your own connections, so set postgres.enabled to false" -}}
{{- end -}}
{{- if not .Values.oidcAudience -}}
{{- fail "oidcAudience is required" -}}
{{- end -}}
{{- end -}}
