# Shared by scripts/a77.sh, a78.sh and monitoring-checks.sh: a deployment's
# folder made only from what a release attaches, and a throwaway registry on
# the job's own machine standing in for GHCR. Images built on a branch go
# there and nowhere else, so `docker compose pull` really pulls them. Sourced.

REGISTRY=127.0.0.1:${PA_REGISTRY_PORT:-15000}
REGISTRY_NAME=pa-registry-$$

registry_start() {
	docker run -d --name "$REGISTRY_NAME" -p "$REGISTRY:5000" \
		registry:3@sha256:d4032d4cc323c1731c707931ecada07a53076eac38e3b61ae57533f170f2c6c3 >/dev/null
	i=0
	until curl -sf "http://$REGISTRY/v2/" >/dev/null; do
		i=$((i + 1))
		[ $i -gt 30 ] && { echo "the throwaway registry didn't start" >&2; return 1; }
		sleep 1
	done
}

registry_stop() {
	docker rm -fv "$REGISTRY_NAME" >/dev/null 2>&1 || true
}

# registry_publish <context folder> <version>: builds the API's image from a
# folder and puts it in the registry under the version. The local tag goes, so
# only a pull can bring it back.
registry_publish() {
	docker build -q -t "$REGISTRY/rikiku/pagerarchive:$2" "$1" >/dev/null
	docker push -q "$REGISTRY/rikiku/pagerarchive:$2" >/dev/null
	docker rmi -f "$REGISTRY/rikiku/pagerarchive:$2" >/dev/null
}

# use_registry <compose file>: the only change a test makes to a release's
# compose.yaml: our image comes from the registry here instead of GHCR.
use_registry() {
	sed -i "s#ghcr\.io/rikiku/#$REGISTRY/rikiku/#" "$1"
}

# fill_env <.env file> <KEY=value file>...: as someone filling in .env would,
# sets each key, whether the line was there, empty or commented out.
fill_env() {
	_file=$1
	shift
	for _vars in "$@"; do
		while IFS= read -r _line; do
			case $_line in "" | "#"*) continue ;; esac
			_key=${_line%%=*}
			sed -i -E "/^#? ?$_key=/d" "$_file"
			printf '%s\n' "$_line" >>"$_file"
		done <"$_vars"
	done
}

# install_folder <folder> <assets folder>: compose.yaml, .env and the unpacked
# archive, nothing else.
install_folder() {
	mkdir -p "$1"
	cp "$2/compose.yaml" "$1/compose.yaml"
	cp "$2/env.example" "$1/.env"
	tar -xzf "$2/pagerarchive-config.tar.gz" -C "$1"
}

# listing <folder>: everything in it, directories included, so a directory
# Docker made for a missing file shows up.
listing() {
	(cd "$1" && find . -mindepth 1 | sort)
}

# in_folder <folder> <compose arguments>: Compose as a deployment runs it, from
# its folder with only .env for settings: nothing the test exported reaches it.
in_folder() {
	_dir=$1
	shift
	(cd "$_dir" && env -i PATH="$PATH" HOME="$HOME" ${DOCKER_HOST:+DOCKER_HOST="$DOCKER_HOST"} \
		${COMPOSE_PROJECT_NAME:+COMPOSE_PROJECT_NAME="$COMPOSE_PROJECT_NAME"} docker compose "$@")
}

# check_images <project> <allowed name>...: every container runs PagerArchive's
# image from the registry at $IMAGE_TAG, or an allowed upstream image, with a
# tag and no digest (R84, A78).
check_images() {
	_project=$1
	shift
	_bad=0
	for _id in $(docker ps -aq --filter "label=com.docker.compose.project=$_project"); do
		_image=$(docker inspect -f '{{.Config.Image}}' "$_id")
		[ "$_image" != "$REGISTRY/rikiku/pagerarchive:$IMAGE_TAG" ] || continue
		_found=""
		for _name in "$@"; do
			case $_image in *@*) ;; "$_name":*) _found=1 ;; esac
		done
		if [ -z "$_found" ]; then
			echo "FAIL: a container runs $_image, which isn't ours or a known upstream one by its tag" >&2
			_bad=1
		fi
	done
	return $_bad
}
