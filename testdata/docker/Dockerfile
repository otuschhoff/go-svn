FROM debian:bookworm-slim

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        apache2 apache2-utils libapache2-mod-svn subversion \
    && rm -rf /var/lib/apt/lists/* \
    && a2enmod dav dav_svn authz_svn auth_basic auth_digest ssl

COPY apache-svn.conf /etc/apache2/conf-available/go-svn.conf
RUN a2enconf go-svn

EXPOSE 3690 8080
