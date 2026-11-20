FROM migrate/migrate:v4.17.1
COPY *.sql /migrations/
COPY entrypoint.sh /usr/local/bin/migrate-entrypoint
ENTRYPOINT ["sh", "/usr/local/bin/migrate-entrypoint"]
