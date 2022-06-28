# DitchNet-web

DitchNet-web is a web application for running DitchNet jobs (i.e. mapping ditch networks using a neural network) written in Go. With only some slight modification the application can easily be adapted to run other jobs.

When a new job is requested by a client it is automatically placed in a queue. The queue is checked every 5 seconds and the job that has been in the queue for the longest is automatically started if the number of currently ongoing jobs is less than the configured maximum. Since DitchNet runs in Docker and requires GPU access, it is only possible to run one job at a time.

## Database

DitchNet-web uses a PostgreSQL database to keep track of jobs. The database contains a single table. PostgreSQL is certainly overkill for this application, but it is very performant and I expect this application may grow in the future.

## Configuration

Configuration is done using a single .json-file, DitchNet-web is then launched with the path to the configuration file as its only argument. An example configuration file is included in this repository, please do not use the default database password. Required configuration options below shown below:

|JSON Key            | Type    | Description                                                                                    |
|---                 |---      |---                                                                                             |
|database_host       | string  | IP-address/hostname of database server                                                         |
|database_port       | integer | The port that the database is listening on (PostgreSQL defult is 5432)                         |
|database_username   | string  | Username of user with read/write access to the database                                        |
|database_password   | string  | Password of the database user                                                                  |
|database_name       | string  | Name of the database                                                                           |
|listen_client       | string  | Address that the web application should accept connections from (HTTP, leave blank for any)    |
|listen_port         | integer | The port that the web application should listen on                                             |
|file_storage_path   | string  | Path to directory where DitchNet-web can store temporary job files                             |
|assets_path         | string  | Path the the directory that contains frontend assets for the web.                              |
|max_concurrent_jobs | integer | Maximum number of concurrent jobs. Can only be set to 1.                                       |
|job_timeout_min     | integer | Maximum number of minutes a job is allowed to run before being killed, to prevent hangs.       |

To enable HTTPS set up DitchNet-web to allow connections from only *localhost* or *127.0.01* and to listen to a port other than *80* or *443*, then set up a reverse proxy using e.g. NGINX. Example configuration using NGINX as a reverse proxy shown below (DitchNet-web is listening on port 5566):

```
server {
    server_name ditchnet.phloem.se;
    listen 443 ssl;
    client_max_body_size 32M;  //Limit request size to 32 megabytes

    location / {
        proxy_pass http://localhost:5566;
    }

    ssl_certificate /path/to/my/ssl/certificate.pem;
    ssl_certificate_key /path/to/my/ssl/private_key.pem;
}

//Forward HTTP to HTTPS
server {
    server_name ditchnet.phloem.se;
    listen 80;

    if ($host = ditchnet.phloem.se) {
        return 301 https://$host$request_uri;
    }

    return 404;
}
```

SSL certificates can be aquired from LetsEncrypt.

## Potential improvements

Since DitchNet runs in a docker container, DitchNet-web must run as a user with docker access. Additionally, since DitchNet is run using the root user inside the docker container, this opens up a potential access route for malicious exploitation. This could potentially be mitigated by altering the DitchNet container image and adding a user without elevated privileges, then running the container as that user.
