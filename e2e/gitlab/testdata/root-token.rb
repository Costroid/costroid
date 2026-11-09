# Creates an API token for the instance's administrator and writes it where
# only GitLab's own user and root inside the container can read it. GitLab has
# no API call that makes the first token, and its OAuth server takes no
# password grant.
user = User.find_by_username!("root")
result = PersonalAccessTokens::CreateService.new(
  current_user: user,
  target_user: user,
  organization_id: user.organization_id || Organizations::Organization.first.id,
  params: { name: "costroid-e2e", scopes: [:api], expires_at: 2.days.from_now }
).execute
raise result.message unless result.success?

path = "/tmp/costroid-e2e-root-token"
File.write("#{path}.partial", result.payload[:personal_access_token].token, perm: 0o600)
File.rename("#{path}.partial", path)
