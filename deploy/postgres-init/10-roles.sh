#!/bin/sh
# Creates the two roles the service needs, on a new data directory only. The
# owner runs migrations and owns every table. The application role owns nothing.
set -eu

psql --no-psqlrc --set ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  --set owner_password="$COSTROID_OWNER_PASSWORD" --set app_password="$COSTROID_APP_PASSWORD" <<'SQL'
CREATE ROLE costroid_owner LOGIN PASSWORD :'owner_password' NOSUPERUSER NOBYPASSRLS;
CREATE ROLE costroid_app LOGIN PASSWORD :'app_password' NOSUPERUSER NOBYPASSRLS;
ALTER DATABASE costroid OWNER TO costroid_owner;
SQL
