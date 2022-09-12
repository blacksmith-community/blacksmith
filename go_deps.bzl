load("@bazel_gazelle//:deps.bzl", "go_repository")

def go_repositories():
    go_repository(
        name = "co_honnef_go_tools",
        importpath = "honnef.co/go/tools",
        sum = "h1:/hemPrYIhOhy8zYrNj+069zDB68us2sMGsfkFJO0iZs=",
        version = "v0.0.0-20190523083050-ea95bdfd59fc",
    )
    go_repository(
        name = "com_github_alecthomas_template",
        importpath = "github.com/alecthomas/template",
        sum = "h1:JYp7IbQjafoB+tBA3gMyHYHrpOtNuDiK/uB5uXxq5wM=",
        version = "v0.0.0-20190718012654-fb15b899a751",
    )
    go_repository(
        name = "com_github_alecthomas_units",
        importpath = "github.com/alecthomas/units",
        sum = "h1:Hs82Z41s6SdL1CELW+XaDYmOH4hkBN4/N9og/AsOv7E=",
        version = "v0.0.0-20190717042225-c3de453c63f4",
    )
    go_repository(
        name = "com_github_armon_circbuf",
        importpath = "github.com/armon/circbuf",
        sum = "h1:QEF07wC0T1rKkctt1RINW/+RMTVmiwxETico2l3gxJA=",
        version = "v0.0.0-20150827004946-bbbad097214e",
    )
    go_repository(
        name = "com_github_armon_consul_api",
        importpath = "github.com/armon/consul-api",
        sum = "h1:G1bPvciwNyF7IUmKXNt9Ak3m6u9DE1rF+RmtIkBpVdA=",
        version = "v0.0.0-20180202201655-eb2c6b5be1b6",
    )
    go_repository(
        name = "com_github_armon_go_metrics",
        importpath = "github.com/armon/go-metrics",
        sum = "h1:8GUt8eRujhVEGZFFEjBj46YV4rDjvGrNxb0KMWYkL2I=",
        version = "v0.0.0-20180917152333-f0300d1749da",
    )
    go_repository(
        name = "com_github_armon_go_radix",
        importpath = "github.com/armon/go-radix",
        sum = "h1:BUAU3CGlLvorLI26FmByPp2eC2qla6E1Tw+scpcg/to=",
        version = "v0.0.0-20180808171621-7fddfc383310",
    )
    go_repository(
        name = "com_github_armon_go_socks5",
        importpath = "github.com/armon/go-socks5",
        sum = "h1:0CwZNZbxp69SHPdPJAN/hZIm0C4OItdklCFmMRWYpio=",
        version = "v0.0.0-20160902184237-e75332964ef5",
    )
    go_repository(
        name = "com_github_aws_aws_sdk_go",
        importpath = "github.com/aws/aws-sdk-go",
        sum = "h1:eTBgLksUJNLk5EwBl/lUweXjBZHbxvfcvqUxAJu7Fqg=",
        version = "v1.34.24",
    )
    go_repository(
        name = "com_github_azure_azure_sdk_for_go",
        importpath = "github.com/Azure/azure-sdk-for-go",
        sum = "h1:U0UFy0NJNmEVACXlAdmI1xNfXosG1RvxNeRV4iEwFX8=",
        version = "v8.1.1-beta+incompatible",
    )
    go_repository(
        name = "com_github_beorn7_perks",
        importpath = "github.com/beorn7/perks",
        sum = "h1:VlbKKnNfV8bJzeqoa4cOKqO6bYr3WgKZxO8Z16+hsOM=",
        version = "v1.0.1",
    )
    go_repository(
        name = "com_github_bgentry_speakeasy",
        importpath = "github.com/bgentry/speakeasy",
        sum = "h1:ByYyxL9InA1OWqxJqqp2A5pYHUrCiAL6K3J+LKSsQkY=",
        version = "v0.1.0",
    )
    go_repository(
        name = "com_github_bmizerany_assert",
        importpath = "github.com/bmizerany/assert",
        sum = "h1:DDGfHa7BWjL4YnC6+E63dPcxHo2sUxDIu8g3QgEJdRY=",
        version = "v0.0.0-20160611221934-b7ed37b82869",
    )
    go_repository(
        name = "com_github_bmizerany_pat",
        importpath = "github.com/bmizerany/pat",
        sum = "h1:y4B3+GPxKlrigF1ha5FFErxK+sr6sWxQovRMzwMhejo=",
        version = "v0.0.0-20170815010413-6226ea591a40",
    )
    go_repository(
        name = "com_github_burntsushi_toml",
        importpath = "github.com/BurntSushi/toml",
        sum = "h1:WXkYYl6Yr3qBf1K79EBnL4mak0OimBfB0XUf9Vl28OQ=",
        version = "v0.3.1",
    )
    go_repository(
        name = "com_github_client9_misspell",
        importpath = "github.com/client9/misspell",
        sum = "h1:ta993UF76GwbvJcIo3Y68y/M3WxlpEHPWIGDkJYwzJI=",
        version = "v0.3.4",
    )
    go_repository(
        name = "com_github_cloudfoundry_community_gogobosh",
        importpath = "github.com/cloudfoundry-community/gogobosh",
        sum = "h1:0lyqFP6UWA1a1E8Ugh5LURdWYra1x5xeOX9p3h6KOA4=",
        version = "v0.0.0-20210826203159-894da954b612",
    )
    go_repository(
        name = "com_github_cloudfoundry_community_vaultkv",
        importpath = "github.com/cloudfoundry-community/vaultkv",
        sum = "h1:O2u6ZVlH9JF3IBSbUYDhX9lAMe0PhVqCKKbz1/lYqk0=",
        version = "v0.3.0",
    )
    go_repository(
        name = "com_github_codegangsta_inject",
        importpath = "github.com/codegangsta/inject",
        sum = "h1:sDMmm+q/3+BukdIpxwO365v/Rbspp2Nt5XntgQRXq8Q=",
        version = "v0.0.0-20150114235600-33e0aa1cb7c0",
    )
    go_repository(
        name = "com_github_coreos_etcd",
        importpath = "github.com/coreos/etcd",
        sum = "h1:Zz1aXgDrFFi1nadh58tA9ktt06cmPTwNNP3dXwIq1lE=",
        version = "v3.3.18+incompatible",
    )
    go_repository(
        name = "com_github_coreos_go_etcd",
        importpath = "github.com/coreos/go-etcd",
        sum = "h1:bXhRBIXoTm9BYHS3gE0TtQuyNZyeEMux2sDi4oo5YOo=",
        version = "v2.0.0+incompatible",
    )
    go_repository(
        name = "com_github_coreos_go_semver",
        importpath = "github.com/coreos/go-semver",
        sum = "h1:3Jm3tLmsgAYcjC+4Up7hJrFBPr+n7rAqYeSw/SZazuY=",
        version = "v0.2.0",
    )
    go_repository(
        name = "com_github_coreos_go_systemd",
        importpath = "github.com/coreos/go-systemd",
        sum = "h1:JOrtw2xFKzlg+cbHpyrpLDmnN1HqhBfnX7WDiW7eG2c=",
        version = "v0.0.0-20190719114852-fd7a80b32e1f",
    )
    go_repository(
        name = "com_github_coreos_pkg",
        importpath = "github.com/coreos/pkg",
        sum = "h1:lBNOc5arjvs8E5mO2tbpBpLoyyu8B6e44T7hJy6potg=",
        version = "v0.0.0-20180928190104-399ea9e2e55f",
    )
    go_repository(
        name = "com_github_cppforlife_go_patch",
        importpath = "github.com/cppforlife/go-patch",
        sum = "h1:Y14MnCQjDlbw7WXT4k+u6DPAA9XnygN4BfrSpI/19RU=",
        version = "v0.2.0",
    )
    go_repository(
        name = "com_github_cpuguy83_go_md2man",
        importpath = "github.com/cpuguy83/go-md2man",
        sum = "h1:BSKMNlYxDvnunlTymqtgONjNnaRV1sTpcovwwjF22jk=",
        version = "v1.0.10",
    )
    go_repository(
        name = "com_github_data_dog_go_sqlmock",
        importpath = "github.com/DATA-DOG/go-sqlmock",
        sum = "h1:CWUqKXe0s8A2z6qCgkP4Kru7wC11YoAnoupUKFDnH08=",
        version = "v1.3.3",
    )
    go_repository(
        name = "com_github_davecgh_go_spew",
        importpath = "github.com/davecgh/go-spew",
        sum = "h1:vj9j/u1bqnvCEfJOwUhtlOARqs3+rkHYY13jYWTU97c=",
        version = "v1.1.1",
    )
    go_repository(
        name = "com_github_drewolson_testflight",
        importpath = "github.com/drewolson/testflight",
        sum = "h1:jgA0pHcFIPnXoBmyFzrdoR2ka4UvReMDsjYc7Jcvl80=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_erikdubbelboer_gspt",
        importpath = "github.com/ErikDubbelboer/gspt",
        sum = "h1:Qs3YRJ9WQ1+Et+uoXBVfKUBzdn9FGJiH98UgfBIiqzk=",
        version = "v0.0.0-20180711091504-e39e726e09cc",
    )
    go_repository(
        name = "com_github_fatih_color",
        importpath = "github.com/fatih/color",
        sum = "h1:DkWD4oS2D8LGGgTQ6IvwJJXSL5Vp2ffcQg58nFV38Ys=",
        version = "v1.7.0",
    )
    go_repository(
        name = "com_github_fsnotify_fsnotify",
        importpath = "github.com/fsnotify/fsnotify",
        sum = "h1:hsms1Qyu0jgnwNXIxa+/V/PDsU6CfLf6CNO8H7IWoS4=",
        version = "v1.4.9",
    )
    go_repository(
        name = "com_github_fsouza_go_dockerclient",
        importpath = "github.com/fsouza/go-dockerclient",
        sum = "h1:WXNYDf0vgVUxX10xfho0rOfeCI3bldEmTqMxkWKLx58=",
        version = "v0.0.0-20151130162558-d750ee8aff39",
    )
    go_repository(
        name = "com_github_geofffranks_simpleyaml",
        importpath = "github.com/geofffranks/simpleyaml",
        sum = "h1:5AjbNPs5ax5Rf1/FeG8tLUYOoEbEDvcMvBgBUH4fDRM=",
        version = "v0.0.0-20161109204137-c9320f076de5",
    )
    go_repository(
        name = "com_github_geofffranks_spruce",
        importpath = "github.com/geofffranks/spruce",
        sum = "h1:Mz/n6CjFVemIVXigtmzErXAzls9NcUNnfbfNEdR9r1U=",
        version = "v1.27.0",
    )
    go_repository(
        name = "com_github_geofffranks_yaml",
        importpath = "github.com/geofffranks/yaml",
        sum = "h1:CxigGHNaNtLTrnMveo9CjJjgXTuZpbLvuOvY/+c1v8g=",
        version = "v0.0.0-20161117152608-9f2fe4b6f295",
    )
    go_repository(
        name = "com_github_go_kit_kit",
        importpath = "github.com/go-kit/kit",
        sum = "h1:wDJmvq38kDhkVxi50ni9ykkdUr1PKgqKOoi01fa0Mdk=",
        version = "v0.9.0",
    )
    go_repository(
        name = "com_github_go_logfmt_logfmt",
        importpath = "github.com/go-logfmt/logfmt",
        sum = "h1:MP4Eh7ZCb31lleYCFuwm0oe4/YGak+5l1vA2NOE80nA=",
        version = "v0.4.0",
    )
    go_repository(
        name = "com_github_go_martini_martini",
        importpath = "github.com/go-martini/martini",
        sum = "h1:xveKWz2iaueeTaUgdetzel+U7exyigDYBryyVfV/rZk=",
        version = "v0.0.0-20170121215854-22fa46961aab",
    )
    go_repository(
        name = "com_github_go_sql_driver_mysql",
        importpath = "github.com/go-sql-driver/mysql",
        sum = "h1:ozyZYNQW3x3HtqT1jira07DN2PArx2v7/mN66gGcHOs=",
        version = "v1.5.0",
    )
    go_repository(
        name = "com_github_go_stack_stack",
        importpath = "github.com/go-stack/stack",
        sum = "h1:5SgMzNM5HxrEjV0ww2lTmX6E2Izsfxas4+YHWRs3Lsk=",
        version = "v1.8.0",
    )
    go_repository(
        name = "com_github_gogo_protobuf",
        importpath = "github.com/gogo/protobuf",
        sum = "h1:X+zN6RZXsvnrSJaAIQhZezPfAfvsqihKKR8oiLHid34=",
        version = "v1.2.2-0.20190730201129-28a6bbf47e48",
    )
    go_repository(
        name = "com_github_golang_glog",
        importpath = "github.com/golang/glog",
        sum = "h1:VKtxabqXZkF25pY9ekfRL6a582T4P37/31XEstQ5p58=",
        version = "v0.0.0-20160126235308-23def4e6c14b",
    )
    go_repository(
        name = "com_github_golang_mock",
        importpath = "github.com/golang/mock",
        sum = "h1:G5FRp8JnTd7RQH5kemVNlMeyXQAztQ3mOWV95KxsXH8=",
        version = "v1.1.1",
    )
    go_repository(
        name = "com_github_golang_protobuf",
        importpath = "github.com/golang/protobuf",
        sum = "h1:+Z5KGCizgyZCbGh1KZqA0fcLLkwbsjIzS4aV2v7wJX0=",
        version = "v1.4.2",
    )
    go_repository(
        name = "com_github_gonvenience_bunt",
        importpath = "github.com/gonvenience/bunt",
        sum = "h1:isYxOpDqbRMOSRhZtoux1tYvhhQ/AIbVDFrs24l6t0M=",
        version = "v1.1.1",
    )
    go_repository(
        name = "com_github_gonvenience_neat",
        importpath = "github.com/gonvenience/neat",
        sum = "h1:DKlFjUuEOoJaYEBejxRhim3T804kXW5ppj/r0+2VvaE=",
        version = "v1.3.0",
    )
    go_repository(
        name = "com_github_gonvenience_term",
        importpath = "github.com/gonvenience/term",
        sum = "h1:joCB/j0Ngmdakd3muuLgAGPMf7DNKdoe708c1I6RiBs=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_gonvenience_text",
        importpath = "github.com/gonvenience/text",
        sum = "h1:Tfy+SHG3dSnUhxwxaiXJci++Ix0YNumoRu0v7yR4w2Q=",
        version = "v1.0.5",
    )
    go_repository(
        name = "com_github_gonvenience_wrap",
        importpath = "github.com/gonvenience/wrap",
        sum = "h1:d8gEZrXS/zg4BC1q0U4nHpPIh5k6muKpQ1+rQFBwpYc=",
        version = "v1.1.0",
    )
    go_repository(
        name = "com_github_gonvenience_ytbx",
        importpath = "github.com/gonvenience/ytbx",
        sum = "h1:jt9Sek7qb2Hk1Z0FcNa+VrtDF7ihA4coY9FLZKCvPEM=",
        version = "v1.2.2",
    )
    go_repository(
        name = "com_github_google_btree",
        importpath = "github.com/google/btree",
        sum = "h1:964Od4U6p2jUkFxvCydnIczKteheJEzHRToSGK3Bnlw=",
        version = "v0.0.0-20180813153112-4030bb1f1f0c",
    )
    go_repository(
        name = "com_github_google_go_cmp",
        importpath = "github.com/google/go-cmp",
        sum = "h1:xsAVV57WRhGj6kEIi8ReJzQlHHqcBYCElAvkovg3B/4=",
        version = "v0.4.0",
    )
    go_repository(
        name = "com_github_google_go_github",
        importpath = "github.com/google/go-github",
        sum = "h1:PJ3M22CS/vkqRasC8rc5/zAeL6yeHkH3aOoiFK1rfq0=",
        version = "v0.0.0-20150605201353-af17a5fa8537",
    )
    go_repository(
        name = "com_github_google_go_querystring",
        importpath = "github.com/google/go-querystring",
        sum = "h1:XScp/7K55BJWgFeKalWP/2XvaRdhhhNJBOSQuFMS8mE=",
        version = "v0.0.0-20150414214848-547ef5ac9797",
    )
    go_repository(
        name = "com_github_google_gofuzz",
        importpath = "github.com/google/gofuzz",
        sum = "h1:A8PeW59pxE9IoFRqBp37U+mSNaQoZ46F1f0f863XSXw=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_google_uuid",
        importpath = "github.com/google/uuid",
        sum = "h1:XXzyYlFbxK3kWfcmu3Wc+Tv8/QQl/VqwsWuSYF1Rj0s=",
        version = "v1.1.2-0.20190416172445-c2e93f3ae59f",
    )
    go_repository(
        name = "com_github_gopherjs_gopherjs",
        importpath = "github.com/gopherjs/gopherjs",
        sum = "h1:l5lAOZEym3oK3SQ2HBHWsJUfbNBiTXJDeW2QDxw9AQ0=",
        version = "v0.0.0-20200217142428-fce0ec30dd00",
    )
    go_repository(
        name = "com_github_gorilla_mux",
        importpath = "github.com/gorilla/mux",
        sum = "h1:gnP5JzjVOuiZD07fKKToCAOjS0yOpj/qPETTXCCS6hw=",
        version = "v1.7.3",
    )
    go_repository(
        name = "com_github_gorilla_websocket",
        importpath = "github.com/gorilla/websocket",
        sum = "h1:ivAVUJb9QD1J3V+NMC1FI677/p2n/MgEquRjhr9opF8=",
        version = "v1.2.1-0.20171123081129-23059f29570f",
    )
    go_repository(
        name = "com_github_hashicorp_consul",
        importpath = "github.com/hashicorp/consul",
        sum = "h1:Z/JG7b8Djn1v4LwLT9YWSTrxMQwTidzT1sD6rJYF9Pc=",
        version = "v0.8.0",
    )
    go_repository(
        name = "com_github_hashicorp_errwrap",
        importpath = "github.com/hashicorp/errwrap",
        sum = "h1:hLrqtEDnRye3+sgx6z4qVLNuviH3MR5aQ0ykNJa/UYA=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_hashicorp_go_cleanhttp",
        importpath = "github.com/hashicorp/go-cleanhttp",
        sum = "h1:dH3aiDG9Jvb5r5+bYHsikaOUIpcM0xvgMXVoDkXMzJM=",
        version = "v0.5.1",
    )
    go_repository(
        name = "com_github_hashicorp_go_immutable_radix",
        importpath = "github.com/hashicorp/go-immutable-radix",
        sum = "h1:AKDB1HM5PWEA7i4nhcpwOrO2byshxBjXVn/J/3+z5/0=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_hashicorp_go_msgpack",
        importpath = "github.com/hashicorp/go-msgpack",
        sum = "h1:zKjpN5BK/P5lMYrLmBHdBULWbJ0XpYR+7NGzqkZzoD4=",
        version = "v0.5.3",
    )
    go_repository(
        name = "com_github_hashicorp_go_multierror",
        importpath = "github.com/hashicorp/go-multierror",
        sum = "h1:iVjPR7a6H0tWELX5NxNe7bYopibicUzc7uPribsnS6o=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_hashicorp_go_sockaddr",
        importpath = "github.com/hashicorp/go-sockaddr",
        sum = "h1:GeH6tui99pF4NJgfnhp+L6+FfobzVW3Ah46sLo0ICXs=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_hashicorp_go_syslog",
        importpath = "github.com/hashicorp/go-syslog",
        sum = "h1:KaodqZuhUoZereWVIYmpUgZysurB1kBLX2j0MwMrUAE=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_hashicorp_go_uuid",
        importpath = "github.com/hashicorp/go-uuid",
        sum = "h1:fv1ep09latC32wFoVwnqcnKJGnMSdBanPczbHAYm1BE=",
        version = "v1.0.1",
    )
    go_repository(
        name = "com_github_hashicorp_go_version",
        importpath = "github.com/hashicorp/go-version",
        sum = "h1:21MVWPKDphxa7ineQQTrCU5brh7OuVVAzGOCnnCPtE8=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_hashicorp_golang_lru",
        importpath = "github.com/hashicorp/golang-lru",
        sum = "h1:CL2msUPvZTLb5O648aiLNJw3hnBxN2+1Jq8rCOH9wdo=",
        version = "v0.5.0",
    )
    go_repository(
        name = "com_github_hashicorp_hcl",
        importpath = "github.com/hashicorp/hcl",
        sum = "h1:0Anlzjpi4vEasTeNFn2mLJgTSwt0+6sfsiTG8qcWGx4=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_hashicorp_logutils",
        importpath = "github.com/hashicorp/logutils",
        sum = "h1:dLEQVugN8vlakKOUE3ihGLTZJRB4j+M2cdTm/ORI65Y=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_hashicorp_mdns",
        importpath = "github.com/hashicorp/mdns",
        sum = "h1:XFSOubp8KWB+Jd2PDyaX5xUd5bhSP/+pTDZVDMzZJM8=",
        version = "v1.0.1",
    )
    go_repository(
        name = "com_github_hashicorp_memberlist",
        importpath = "github.com/hashicorp/memberlist",
        sum = "h1:WeeNspppWi5s1OFefTviPQueC/Bq8dONfvNjPhiEQKE=",
        version = "v0.2.0",
    )
    go_repository(
        name = "com_github_hashicorp_serf",
        importpath = "github.com/hashicorp/serf",
        sum = "h1:+Zd/16AJ9lxk9RzfTDyv/TLhZ8UerqYS0/+JGCIDaa0=",
        version = "v0.9.0",
    )
    go_repository(
        name = "com_github_homeport_dyff",
        importpath = "github.com/homeport/dyff",
        sum = "h1:BlqDugM0NmgmYbjGrZKIp3f2Okyff/o/zp+sJ9p4sjE=",
        version = "v1.0.2",
    )
    go_repository(
        name = "com_github_hpcloud_tail",
        importpath = "github.com/hpcloud/tail",
        sum = "h1:nfCOvKYfkgYP8hkirhJocXT2+zOD8yUNjXaWfTlyFKI=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_inconshreveable_mousetrap",
        importpath = "github.com/inconshreveable/mousetrap",
        sum = "h1:Z8tu5sraLXCXIcARxBp/8cbvlwVa7Z1NHg9XEKhtSvM=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_jhunt_go_ansi",
        importpath = "github.com/jhunt/go-ansi",
        sum = "h1:qAlBkfBj+4aW68SdlhlAiWx+hy7vmchlcAZQRAJFhFI=",
        version = "v0.0.0-20181127194324-5fd839f108b6",
    )
    go_repository(
        name = "com_github_jhunt_go_cli",
        importpath = "github.com/jhunt/go-cli",
        sum = "h1:pJkfnav319gBFbol0av+PamHMB2BZ/zocxt6V2dH6+M=",
        version = "v0.0.0-20180120230054-44398e595118",
    )
    go_repository(
        name = "com_github_jhunt_go_envirotron",
        importpath = "github.com/jhunt/go-envirotron",
        sum = "h1:C2Z+D/WhnixDp/q8+MoyqUlhZIAJvFhWwsGOjJqBG2s=",
        version = "v0.0.0-20191007155228-c8f2a184ad0f",
    )
    go_repository(
        name = "com_github_jhunt_go_log",
        importpath = "github.com/jhunt/go-log",
        sum = "h1:uWIN5FcXeUrXtbR3plzgVsuw4k8L5r+Smi36N2RJ9LQ=",
        version = "v0.0.0-20171024033145-ddc1e3b8ed30",
    )
    go_repository(
        name = "com_github_jhunt_go_querytron",
        importpath = "github.com/jhunt/go-querytron",
        sum = "h1:de0MFgMiZfWX+B+XJM2LkPFkWOh6/1WA5OJMZenf3cM=",
        version = "v0.0.0-20190121150331-d03b28210bbc",
    )
    go_repository(
        name = "com_github_jhunt_go_s3",
        importpath = "github.com/jhunt/go-s3",
        sum = "h1:ekqEwms7BfsaX1p3YrHdD2/afRmLaTvR1MsTtxwynx0=",
        version = "v0.0.0-20190122180757-14f44ecac95f",
    )
    go_repository(
        name = "com_github_jhunt_go_snapshot",
        importpath = "github.com/jhunt/go-snapshot",
        sum = "h1:mGjsfupyBHFg4Gk01Oxw3OHqiBLnIGYbLbaYWRebmgc=",
        version = "v0.0.0-20171017043618-9ad8f5ee37a2",
    )
    go_repository(
        name = "com_github_jhunt_go_table",
        importpath = "github.com/jhunt/go-table",
        sum = "h1:/NpaWylgofckqOik/NjbRKlcVRWwVt1j0YiSbkIO2Mo=",
        version = "v0.0.0-20181127194439-fcc252a20f4c",
    )
    go_repository(
        name = "com_github_jmespath_go_jmespath",
        importpath = "github.com/jmespath/go-jmespath",
        sum = "h1:OS12ieG61fsCg5+qLJ+SsW9NicxNkg3b25OyT2yCeUc=",
        version = "v0.3.0",
    )
    go_repository(
        name = "com_github_jmoiron_sqlx",
        importpath = "github.com/jmoiron/sqlx",
        sum = "h1:O3uSi37r0VhywMlg5lN/eB2LwYnBLhLbfXHxpMVNcWk=",
        version = "v0.0.0-20160615151803-bdae0c3219c3",
    )
    go_repository(
        name = "com_github_json_iterator_go",
        importpath = "github.com/json-iterator/go",
        sum = "h1:KfgG9LzI+pYjr4xvmz/5H4FXjokeP+rlHLhv3iH62Fo=",
        version = "v1.1.7",
    )
    go_repository(
        name = "com_github_jtolds_gls",
        importpath = "github.com/jtolds/gls",
        sum = "h1:xdiiI2gbIgH/gLH7ADydsJ1uDOEzR8yvV7C0MuV77Wo=",
        version = "v4.20.0+incompatible",
    )
    go_repository(
        name = "com_github_julienschmidt_httprouter",
        importpath = "github.com/julienschmidt/httprouter",
        sum = "h1:TDTW5Yz1mjftljbcKqRcrYhd4XeOoI98t+9HbQbYf7g=",
        version = "v1.2.0",
    )
    go_repository(
        name = "com_github_kisielk_errcheck",
        importpath = "github.com/kisielk/errcheck",
        sum = "h1:reN85Pxc5larApoH1keMBiu2GWtPqXQ1nc9gx+jOU+E=",
        version = "v1.2.0",
    )
    go_repository(
        name = "com_github_kisielk_gotool",
        importpath = "github.com/kisielk/gotool",
        sum = "h1:AV2c/EiW3KqPNT9ZKl07ehoAGi4C5/01Cfbblndcapg=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_knetic_govaluate",
        importpath = "github.com/Knetic/govaluate",
        sum = "h1:7o6+MAPhYTCF0+fdvoz1xDedhRb4f6s9Tn1Tt7/WTEg=",
        version = "v3.0.0+incompatible",
    )
    go_repository(
        name = "com_github_konsorten_go_windows_terminal_sequences",
        importpath = "github.com/konsorten/go-windows-terminal-sequences",
        sum = "h1:mweAR1A6xJ3oS2pRaGiHgQ4OO8tzTaLawm8vnODuwDk=",
        version = "v1.0.1",
    )
    go_repository(
        name = "com_github_kr_logfmt",
        importpath = "github.com/kr/logfmt",
        sum = "h1:T+h1c/A9Gawja4Y9mFVWj2vyii2bbUNDw3kt9VxK2EY=",
        version = "v0.0.0-20140226030751-b84e30acd515",
    )
    go_repository(
        name = "com_github_kr_pretty",
        importpath = "github.com/kr/pretty",
        sum = "h1:L/CwN0zerZDmRFUapSPitk6f+Q3+0za1rQkzVuMiMFI=",
        version = "v0.1.0",
    )
    go_repository(
        name = "com_github_kr_pty",
        importpath = "github.com/kr/pty",
        sum = "h1:VkoXIwSboBpnk99O/KFauAEILuNHv5DVFKZMBN/gUgw=",
        version = "v1.1.1",
    )
    go_repository(
        name = "com_github_kr_text",
        importpath = "github.com/kr/text",
        sum = "h1:45sCR5RtlFHMR4UwH9sdQ5TC8v0qDQCHnXt+kaKSTVE=",
        version = "v0.1.0",
    )
    go_repository(
        name = "com_github_kurin_blazer",
        importpath = "github.com/kurin/blazer",
        sum = "h1:mBc4i1uhHJEqU0KvzOgpMHhkwf+EcXvxjWEUS7HG+eY=",
        version = "v0.5.1",
    )
    go_repository(
        name = "com_github_lucasb_eyer_go_colorful",
        importpath = "github.com/lucasb-eyer/go-colorful",
        sum = "h1:QIbQXiugsb+q10B+MI+7DI1oQLdmnep86tWFlaaUAac=",
        version = "v1.0.3",
    )
    go_repository(
        name = "com_github_magiconair_properties",
        importpath = "github.com/magiconair/properties",
        sum = "h1:LLgXmsheXeRoUOBOjtwPQCWIYqM/LU1ayDtDePerRcY=",
        version = "v1.8.0",
    )
    go_repository(
        name = "com_github_martini_contrib_render",
        importpath = "github.com/martini-contrib/render",
        sum = "h1:YFh+sjyJTMQSYjKwM4dFKhJPJC/wfo98tPUc17HdoYw=",
        version = "v0.0.0-20150707142108-ec18f8345a11",
    )
    go_repository(
        name = "com_github_mattn_go_ciede2000",
        importpath = "github.com/mattn/go-ciede2000",
        sum = "h1:BXxTozrOU8zgC5dkpn3J6NTRdoP+hjok/e+ACr4Hibk=",
        version = "v0.0.0-20170301095244-782e8c62fec3",
    )
    go_repository(
        name = "com_github_mattn_go_colorable",
        importpath = "github.com/mattn/go-colorable",
        sum = "h1:UVL0vNpWh04HeJXV0KLcaT7r06gOH2l4OW6ddYRUIY4=",
        version = "v0.0.9",
    )
    go_repository(
        name = "com_github_mattn_go_isatty",
        importpath = "github.com/mattn/go-isatty",
        sum = "h1:wuysRhFDzyxgEmMf5xjvJ2M9dZoWAXNNr5LSBS7uHXY=",
        version = "v0.0.12",
    )
    go_repository(
        name = "com_github_mattn_go_shellwords",
        importpath = "github.com/mattn/go-shellwords",
        sum = "h1:xlTU5yhz4gG9QkPtaLZeDXCbsEkX+Ve2rxmVmG5PdSs=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_mattn_go_sqlite3",
        importpath = "github.com/mattn/go-sqlite3",
        sum = "h1:SsTB8sm889/q4vKNvlWbc++vrUmcSznpCXkoyE6HNi0=",
        version = "v1.1.1-0.20161028142218-86681de00ade",
    )
    go_repository(
        name = "com_github_matttproud_golang_protobuf_extensions",
        importpath = "github.com/matttproud/golang_protobuf_extensions",
        sum = "h1:I0XW9+e1XWDxdcEniV4rQAIOPUGDq67JSCiRCgGCZLI=",
        version = "v1.0.2-0.20181231171920-c182affec369",
    )
    go_repository(
        name = "com_github_miekg_dns",
        importpath = "github.com/miekg/dns",
        sum = "h1:gPxPSwALAeHJSjarOs00QjVdV9QoBvc1D2ujQUr5BzU=",
        version = "v1.1.26",
    )
    go_repository(
        name = "com_github_mitchellh_cli",
        importpath = "github.com/mitchellh/cli",
        sum = "h1:iGBIsUe3+HZ/AD/Vd7DErOt5sU9fa8Uj7A2s1aggv1Y=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_mitchellh_go_homedir",
        importpath = "github.com/mitchellh/go-homedir",
        sum = "h1:lukF9ziXFxDFPkA1vsr5zpc1XuPDn/wFntq5mG+4E0Y=",
        version = "v1.1.0",
    )
    go_repository(
        name = "com_github_mitchellh_go_ps",
        importpath = "github.com/mitchellh/go-ps",
        sum = "h1:i6ampVEEF4wQFF+bkYfwYgY+F/uYJDktmvLPf7qIgjc=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_mitchellh_gox",
        importpath = "github.com/mitchellh/gox",
        sum = "h1:x0jD3dcHk9a9xPSDN6YEL4xL6Qz0dvNYm8yZqui5chI=",
        version = "v1.0.1",
    )
    go_repository(
        name = "com_github_mitchellh_hashstructure",
        importpath = "github.com/mitchellh/hashstructure",
        sum = "h1:ZkRJX1CyOoTkar7p/mLS5TZU4nJ1Rn/F8u9dGS02Q3Y=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_mitchellh_iochan",
        importpath = "github.com/mitchellh/iochan",
        sum = "h1:C+X3KsSTLFVBr/tK1eYN/vs4rJcvsiLU338UhYPJWeY=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_mitchellh_mapstructure",
        importpath = "github.com/mitchellh/mapstructure",
        sum = "h1:fmNYVwqnSfB9mZU6OS2O6GsXM+wcskZDuKQzvN1EDeE=",
        version = "v1.1.2",
    )
    go_repository(
        name = "com_github_modern_go_concurrent",
        importpath = "github.com/modern-go/concurrent",
        sum = "h1:TRLaZ9cD/w8PVh93nsPXa1VrQ6jlwL5oN8l14QlcNfg=",
        version = "v0.0.0-20180306012644-bacd9c7ef1dd",
    )
    go_repository(
        name = "com_github_modern_go_reflect2",
        importpath = "github.com/modern-go/reflect2",
        sum = "h1:9f412s+6RmYXLWZSEzVVgPGK7C2PphHj5RJrvfx9AWI=",
        version = "v1.0.1",
    )
    go_repository(
        name = "com_github_mwitkow_go_conntrack",
        importpath = "github.com/mwitkow/go-conntrack",
        sum = "h1:F9x/1yl3T2AeKLr2AMdilSD8+f9bvMnNN8VS5iDtovc=",
        version = "v0.0.0-20161129095857-cc309e4a2223",
    )
    go_repository(
        name = "com_github_ncw_swift",
        importpath = "github.com/ncw/swift",
        sum = "h1:t2kC2L3N+IG8J8eoaGCZu3yHo3Q/YATUyNqUKC0/ni8=",
        version = "v1.0.48-0.20190410202254-753d2090bb62",
    )
    go_repository(
        name = "com_github_niemeyer_pretty",
        importpath = "github.com/niemeyer/pretty",
        sum = "h1:fD57ERR4JtEqsWbfPhv4DMiApHyliiK5xCTNVSPiaAs=",
        version = "v0.0.0-20200227124842-a10e7caefd8e",
    )
    go_repository(
        name = "com_github_nxadm_tail",
        importpath = "github.com/nxadm/tail",
        sum = "h1:DQuhQpB1tVlglWS2hLQ5OV6B5r8aGxSrPc5Qo6uTN78=",
        version = "v1.4.4",
    )
    go_repository(
        name = "com_github_onsi_ginkgo",
        importpath = "github.com/onsi/ginkgo",
        sum = "h1:M76yO2HkZASFjXL0HSoZJ1AYEmQxNJmY41Jx1zNUq1Y=",
        version = "v1.13.0",
    )
    go_repository(
        name = "com_github_onsi_gomega",
        importpath = "github.com/onsi/gomega",
        sum = "h1:o0+MgICZLuZ7xjH7Vx6zS/zcu93/BEp1VwkIW1mEXCE=",
        version = "v1.10.1",
    )
    go_repository(
        name = "com_github_oxtoacart_bpool",
        importpath = "github.com/oxtoacart/bpool",
        sum = "h1:rp5dCmg/yLR3mgFuSOe4oEnDDmGLROTvMragMUXpTQw=",
        version = "v0.0.0-20190530202638-03653db5a59c",
    )
    go_repository(
        name = "com_github_pascaldekloe_goe",
        importpath = "github.com/pascaldekloe/goe",
        sum = "h1:Lgl0gzECD8GnQ5QCWA8o6BtfL6mDH5rQgM4/fX3avOs=",
        version = "v0.0.0-20180627143212-57f6aae5913c",
    )
    go_repository(
        name = "com_github_pborman_uuid",
        importpath = "github.com/pborman/uuid",
        sum = "h1:+ZZIw58t/ozdjRaXh/3awHfmWRbzYxJoAdNJxe/3pvw=",
        version = "v1.2.1",
    )
    go_repository(
        name = "com_github_pelletier_go_toml",
        importpath = "github.com/pelletier/go-toml",
        sum = "h1:T5zMGML61Wp+FlcbWjRDT7yAxhJNAiPPLOFECq181zc=",
        version = "v1.2.0",
    )
    go_repository(
        name = "com_github_pivotal_cf_brokerapi",
        importpath = "github.com/pivotal-cf/brokerapi",
        sum = "h1:n5rnqjkF2WKL//n/ISQKc7EEgyjn/aasxnGtDZ5REdQ=",
        version = "v0.0.0-20160426160527-3abc5864a39f",
    )
    go_repository(
        name = "com_github_pivotal_golang_lager",
        importpath = "github.com/pivotal-golang/lager",
        sum = "h1:W3IDd4BAARuTmQq9gPTz3mOVaACJfsI5y8bL0zKSu5I=",
        version = "v0.0.0-20160311180000-7639e31ce662",
    )
    go_repository(
        name = "com_github_pkg_errors",
        importpath = "github.com/pkg/errors",
        sum = "h1:FEBLx1zS214owpjy7qsBeixbURkuhQAwrK5UwLGTwt4=",
        version = "v0.9.1",
    )
    go_repository(
        name = "com_github_pmezard_go_difflib",
        importpath = "github.com/pmezard/go-difflib",
        sum = "h1:4DBwDE0NGyQoBHbLQYPwSUPoCMWR5BEzIk/f1lZbAQM=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_posener_complete",
        importpath = "github.com/posener/complete",
        sum = "h1:ccV59UEOTzVDnDUEFdT95ZzHVZ+5+158q8+SJb2QV5w=",
        version = "v1.1.1",
    )
    go_repository(
        name = "com_github_prometheus_client_golang",
        importpath = "github.com/prometheus/client_golang",
        sum = "h1:BQ53HtBmfOitExawJ6LokA4x8ov/z0SYYb0+HxJfRI8=",
        version = "v1.1.0",
    )
    go_repository(
        name = "com_github_prometheus_client_model",
        importpath = "github.com/prometheus/client_model",
        sum = "h1:S/YWwWx/RA8rT8tKFRuGUZhuA90OyIBpPCXkcbwU8DE=",
        version = "v0.0.0-20190129233127-fd36f4220a90",
    )
    go_repository(
        name = "com_github_prometheus_common",
        importpath = "github.com/prometheus/common",
        sum = "h1:RizCxzUblyb3+gYxMwJrdomGLi4AsvQ0x9Q1+KEZx+I=",
        version = "v0.6.1-0.20190730175846-637d7c34db12",
    )
    go_repository(
        name = "com_github_prometheus_procfs",
        importpath = "github.com/prometheus/procfs",
        sum = "h1:uqK/YnaVFq1uofHlzj+IR4HhCYA/nbrvJ431l7cm7Vs=",
        version = "v0.0.4-0.20190731153504-5da962fa40f1",
    )
    go_repository(
        name = "com_github_russross_blackfriday",
        importpath = "github.com/russross/blackfriday",
        sum = "h1:HyvC0ARfnZBqnXwABFeSZHpKvJHJJfPz81GNueLj0oo=",
        version = "v1.5.2",
    )
    go_repository(
        name = "com_github_ryanuber_columnize",
        importpath = "github.com/ryanuber/columnize",
        sum = "h1:UFr9zpz4xgTnIE5yIMtWAMngCdZ9p/+q6lTbgelo80M=",
        version = "v0.0.0-20160712163229-9b3edd62028f",
    )
    go_repository(
        name = "com_github_sclevine_agouti",
        importpath = "github.com/sclevine/agouti",
        sum = "h1:8IBJS6PWz3uTlMP3YBIR5f+KAldcGuOeFkFbUWfBgK4=",
        version = "v3.0.0+incompatible",
    )
    go_repository(
        name = "com_github_sean_seed",
        importpath = "github.com/sean-/seed",
        sum = "h1:nn5Wsu0esKSJiIVhscUtVbo7ada43DJhG55ua/hjS5I=",
        version = "v0.0.0-20170313163322-e2103e2c3529",
    )
    go_repository(
        name = "com_github_sergi_go_diff",
        importpath = "github.com/sergi/go-diff",
        sum = "h1:we8PVUC3FE2uYfodKH/nBHMSetSfHDR6scGdBi+erh0=",
        version = "v1.1.0",
    )
    go_repository(
        name = "com_github_shieldproject_shield",
        importpath = "github.com/shieldproject/shield",
        sum = "h1:8BNkOvaOtWXfo093NP0f6lkJ4I6iOHHYjkyVcdPKd4c=",
        version = "v0.10.10-0.20220624132236-7b2f20a37a18",
    )
    go_repository(
        name = "com_github_sirupsen_logrus",
        importpath = "github.com/sirupsen/logrus",
        sum = "h1:SPIRibHv4MatM3XXNO2BJeFLZwZ2LvZgfQ5+UNI2im4=",
        version = "v1.4.2",
    )
    go_repository(
        name = "com_github_smallfish_simpleyaml",
        importpath = "github.com/smallfish/simpleyaml",
        sum = "h1:F5zGAASXYKmF84QIFczbEnjEsuNn/bzlcO+DvxbG2T4=",
        version = "v0.0.0-20160128122539-e0865b8fe0bb",
    )
    go_repository(
        name = "com_github_smartystreets_assertions",
        importpath = "github.com/smartystreets/assertions",
        sum = "h1:hBSHahWMEgzwRyS6dRpxY0XyjZsHyQ61s084wo5PJe0=",
        version = "v0.0.0-20190401211740-f487f9de1cd3",
    )
    go_repository(
        name = "com_github_smartystreets_goconvey",
        importpath = "github.com/smartystreets/goconvey",
        sum = "h1:fv0U8FUIMPNf1L9lnHLvLhgicrIVChEkdzIKYqbNC9s=",
        version = "v1.6.4",
    )
    go_repository(
        name = "com_github_spf13_afero",
        importpath = "github.com/spf13/afero",
        sum = "h1:m8/z1t7/fwjysjQRYbP0RD+bUIF/8tJwPdEZsI83ACI=",
        version = "v1.1.2",
    )
    go_repository(
        name = "com_github_spf13_cast",
        importpath = "github.com/spf13/cast",
        sum = "h1:oget//CVOEoFewqQxwr0Ej5yjygnqGkvggSE/gB35Q8=",
        version = "v1.3.0",
    )
    go_repository(
        name = "com_github_spf13_cobra",
        importpath = "github.com/spf13/cobra",
        sum = "h1:f0B+LkLX6DtmRH1isoNA9VTtNUK9K8xYd28JNNfOv/s=",
        version = "v0.0.5",
    )
    go_repository(
        name = "com_github_spf13_jwalterweatherman",
        importpath = "github.com/spf13/jwalterweatherman",
        sum = "h1:XHEdyB+EcvlqZamSM4ZOMGlc93t6AcsBEu9Gc1vn7yk=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_spf13_pflag",
        importpath = "github.com/spf13/pflag",
        sum = "h1:zPAT6CGy6wXeQ7NtTnaTerfKOsV6V6F8agHXFiazDkg=",
        version = "v1.0.3",
    )
    go_repository(
        name = "com_github_spf13_viper",
        importpath = "github.com/spf13/viper",
        sum = "h1:VUFqw5KcqRf7i70GOzW7N+Q7+gxVBkSSqiXB12+JQ4M=",
        version = "v1.3.2",
    )
    go_repository(
        name = "com_github_starkandwayne_goutils",
        importpath = "github.com/starkandwayne/goutils",
        sum = "h1:vV6o1C8iPioC0Ahi3e9Bs9vVPW9/YN3uwgA6EFahAws=",
        version = "v0.0.0-20190115202530-896b8a6904be",
    )
    go_repository(
        name = "com_github_starkandwayne_safe",
        importpath = "github.com/starkandwayne/safe",
        sum = "h1:lHUfcxhIo2upMSdH2vA8hEJwYubz3+KmGZHxkXmnhYU=",
        version = "v1.5.8",
    )
    go_repository(
        name = "com_github_stretchr_objx",
        importpath = "github.com/stretchr/objx",
        sum = "h1:2vfRuCMp5sSVIDSqO8oNnWJq7mPa6KVP3iPIwFBuy8A=",
        version = "v0.1.1",
    )
    go_repository(
        name = "com_github_stretchr_testify",
        importpath = "github.com/stretchr/testify",
        sum = "h1:nOGnQDM7FYENwehXlg/kFVnos3rEvtKTjRvOWSzb6H4=",
        version = "v1.5.1",
    )
    go_repository(
        name = "com_github_texttheater_golang_levenshtein",
        importpath = "github.com/texttheater/golang-levenshtein",
        sum = "h1:9VTskZOIRf2vKF3UL8TuWElry5pgUpV1tFSe/e/0m/E=",
        version = "v0.0.0-20191208221605-eb6844b05fc6",
    )
    go_repository(
        name = "com_github_tredoe_fileutil",
        importpath = "github.com/tredoe/fileutil",
        sum = "h1:jue1YpVb8cW/HZJZPIC7aOgxhjxazAJdmoixGuBvRvk=",
        version = "v1.0.0",
    )
    go_repository(
        name = "com_github_tredoe_goutil",
        importpath = "github.com/tredoe/goutil",
        sum = "h1:/s7eaZTsft35wEeGOjV4U3DHZ6jwr0MxehKZoMJAFZU=",
        version = "v0.0.0-20200111155331-68cefb6d3cdc",
    )
    go_repository(
        name = "com_github_tredoe_osutil",
        importpath = "github.com/tredoe/osutil",
        sum = "h1:mfXjHBJU46GoJDOUcHyV895fauUuVikR9U8yRbGBrqw=",
        version = "v1.0.5",
    )
    go_repository(
        name = "com_github_ugorji_go_codec",
        importpath = "github.com/ugorji/go/codec",
        sum = "h1:3SVOIvH7Ae1KRYyQWRjXWJEA9sS/c/pjvH++55Gr648=",
        version = "v0.0.0-20181204163529-d75b2dcb6bc8",
    )
    go_repository(
        name = "com_github_virtuald_go_ordered_json",
        importpath = "github.com/virtuald/go-ordered-json",
        sum = "h1:JwtAtbp7r/7QSyGz8mKUbYJBg2+6Cd7OjM8o/GNOcVo=",
        version = "v0.0.0-20170621173500-b18e6e673d74",
    )
    go_repository(
        name = "com_github_voxelbrain_goptions",
        importpath = "github.com/voxelbrain/goptions",
        sum = "h1:9wj8kG7h9BFfuAGtEgtShrnTNY27UI/v6/RGEpue+LA=",
        version = "v0.0.0-20151102231003-26cb8b046923",
    )
    go_repository(
        name = "com_github_xordataexchange_crypt",
        importpath = "github.com/xordataexchange/crypt",
        sum = "h1:ESFSdwYZvkeru3RtdrYueztKhOBCSAAzS4Gf+k0tEow=",
        version = "v0.0.3-0.20170626215501-b2862e3d0a77",
    )
    go_repository(
        name = "com_github_ziutek_utils",
        importpath = "github.com/ziutek/utils",
        sum = "h1:PyI4qg2zvSToKuMdr0WiwbsKkKzyKQBwhELU01zOcfg=",
        version = "v0.0.0-20190626152656-eb2a3b364d6c",
    )
    go_repository(
        name = "com_google_cloud_go",
        importpath = "cloud.google.com/go",
        sum = "h1:e0WKqKTd5BnrG8aKH3J3h+QvEIQtSUcf2n5UZ5ZgLtQ=",
        version = "v0.26.0",
    )
    go_repository(
        name = "in_gopkg_alecthomas_kingpin_v2",
        importpath = "gopkg.in/alecthomas/kingpin.v2",
        sum = "h1:jMFz6MfLP0/4fUyZle81rXUoxOBFi19VUFKVDOQfozc=",
        version = "v2.2.6",
    )
    go_repository(
        name = "in_gopkg_check_v1",
        importpath = "gopkg.in/check.v1",
        sum = "h1:BLraFXnmrev5lT+xlilqcH8XK9/i0At2xKjWk4p6zsU=",
        version = "v1.0.0-20200227125254-8fa46927fb4f",
    )
    go_repository(
        name = "in_gopkg_fsnotify_v1",
        importpath = "gopkg.in/fsnotify.v1",
        sum = "h1:xOHLXZwVvI9hhs+cLKq5+I5onOuwQLhQwiu63xxlHs4=",
        version = "v1.4.7",
    )
    go_repository(
        name = "in_gopkg_tomb_v1",
        importpath = "gopkg.in/tomb.v1",
        sum = "h1:uRGJdciOHaEIrze2W8Q3AKkepLTh2hOroT7a+7czfdQ=",
        version = "v1.0.0-20141024135613-dd632973f1e7",
    )
    go_repository(
        name = "in_gopkg_yaml_v2",
        importpath = "gopkg.in/yaml.v2",
        sum = "h1:D8xgwECY7CYvx+Y2n4sBz93Jn9JRvxdiyyo8CTfuKaY=",
        version = "v2.4.0",
    )
    go_repository(
        name = "in_gopkg_yaml_v3",
        importpath = "gopkg.in/yaml.v3",
        sum = "h1:OfFoIUYv/me30yv7XlMy4F9RJw8DEm8WQ6QG1Ph4bH0=",
        version = "v3.0.0-20200506231410-2ff61e1afc86",
    )
    go_repository(
        name = "io_etcd_go_etcd",
        importpath = "go.etcd.io/etcd",
        sum = "h1:5aomL5mqoKHxw6NG+oYgsowk8tU8aOalo2IdZxdWHkw=",
        version = "v3.3.18+incompatible",
    )
    go_repository(
        name = "org_golang_google_api",
        importpath = "google.golang.org/api",
        sum = "h1:O54iO6ytDQVa09CmIDMFbR7fi1MvMDEfGsmErh6bkR8=",
        version = "v0.0.0-20160107235428-77e7d383beb9",
    )
    go_repository(
        name = "org_golang_google_appengine",
        importpath = "google.golang.org/appengine",
        sum = "h1:/wp5JvzpHIxhs/dumFmF7BXTf3Z+dd4uXta4kVyO508=",
        version = "v1.4.0",
    )
    go_repository(
        name = "org_golang_google_cloud",
        importpath = "google.golang.org/cloud",
        sum = "h1:M0RlP4gJyvKsuSHcBM4g99Dy3I60HTSX5hffdn3oLHk=",
        version = "v0.0.0-20160324202040-eb47ba841d53",
    )
    go_repository(
        name = "org_golang_google_genproto",
        importpath = "google.golang.org/genproto",
        sum = "h1:iKtrH9Y8mcbADOP0YFaEMth7OfuHY9xHOwNj4znpM1A=",
        version = "v0.0.0-20190801165951-fa694d86fc64",
    )
    go_repository(
        name = "org_golang_google_grpc",
        importpath = "google.golang.org/grpc",
        sum = "h1:cfg4PD8YEdSFnm7qLV4++93WcmhH2nIUhMjhdCvl3j8=",
        version = "v1.19.0",
    )
    go_repository(
        name = "org_golang_google_protobuf",
        importpath = "google.golang.org/protobuf",
        sum = "h1:4MY060fB1DLGMB/7MBTLnwQUY6+F09GEiz6SsrNqyzM=",
        version = "v1.23.0",
    )
    go_repository(
        name = "org_golang_x_crypto",
        importpath = "golang.org/x/crypto",
        sum = "h1:DN0cp81fZ3njFcrLCytUHRSUkqBjfTo4Tx9RJTWs0EY=",
        version = "v0.0.0-20201221181555-eec23a3978ad",
    )
    go_repository(
        name = "org_golang_x_exp",
        importpath = "golang.org/x/exp",
        sum = "h1:c2HOrn5iMezYjSlGPncknSEr/8x5LELb/ilJbXi9DEA=",
        version = "v0.0.0-20190121172915-509febef88a4",
    )
    go_repository(
        name = "org_golang_x_lint",
        importpath = "golang.org/x/lint",
        sum = "h1:XQyxROzUlZH+WIQwySDgnISgOivlhjIEwaQaJEJrrN0=",
        version = "v0.0.0-20190313153728-d0100b6bd8b3",
    )
    go_repository(
        name = "org_golang_x_net",
        importpath = "golang.org/x/net",
        sum = "h1:iFwSg7t5GZmB/Q5TjiEAsdoLDrdJRC1RiF2WhuV29Qw=",
        version = "v0.0.0-20201224014010-6772e930b67b",
    )
    go_repository(
        name = "org_golang_x_oauth2",
        importpath = "golang.org/x/oauth2",
        sum = "h1:vEDujvNQGv4jgYKudGeI/+DAX4Jffq6hpD55MmoEvKs=",
        version = "v0.0.0-20180821212333-d2e6202438be",
    )
    go_repository(
        name = "org_golang_x_sync",
        importpath = "golang.org/x/sync",
        sum = "h1:WXEvlFVvvGxCJLG6REjsT03iWnKLEWinaScsxF2Vm2o=",
        version = "v0.0.0-20200317015054-43a5402ce75a",
    )
    go_repository(
        name = "org_golang_x_sys",
        importpath = "golang.org/x/sys",
        sum = "h1:nVuTkr9L6Bq62qpUqKo/RnZCFfzDBL0bYo6w9OJUqZY=",
        version = "v0.0.0-20210113181707-4bcb84eeeb78",
    )
    go_repository(
        name = "org_golang_x_term",
        importpath = "golang.org/x/term",
        sum = "h1:MZ2shdL+ZM/XzY3ZGOnh4Nlpnxz5GSOhOmtHo3iPU6M=",
        version = "v0.0.0-20201210144234-2321bbc49cbf",
    )
    go_repository(
        name = "org_golang_x_text",
        importpath = "golang.org/x/text",
        sum = "h1:i6eZZ+zk0SOf0xgBpEpPD18qWcJda6q1sxt3S0kzyUQ=",
        version = "v0.3.5",
    )
    go_repository(
        name = "org_golang_x_tools",
        importpath = "golang.org/x/tools",
        sum = "h1:xFbv3LvlvQAmbNJFCBKRv1Ccvnh9FVsW0FX2kTWWowE=",
        version = "v0.0.0-20190907020128-2ca718005c18",
    )
    go_repository(
        name = "org_golang_x_xerrors",
        importpath = "golang.org/x/xerrors",
        sum = "h1:E7g+9GITq07hpfrRu66IVDexMakfv52eLZ2CXBWiKr4=",
        version = "v0.0.0-20191204190536-9bdfabe68543",
    )
    go_repository(
        name = "org_uber_go_atomic",
        importpath = "go.uber.org/atomic",
        sum = "h1:cci0ta2RtIAZhvzGR+JH2Lq7Xcg1kNYmHvLCOZYrmbU=",
        version = "v1.4.1-0.20190731194737-ef0d20d85b01",
    )
    go_repository(
        name = "org_uber_go_multierr",
        importpath = "go.uber.org/multierr",
        sum = "h1:Q0q3m6foxW9fUIiNtElisPPzFNmxZ6jxL00xve0A348=",
        version = "v1.1.1-0.20190429210458-bd075f90b08f",
    )
    go_repository(
        name = "org_uber_go_zap",
        importpath = "go.uber.org/zap",
        sum = "h1:fRtzhL15Tocngn2WZQ91iA2apveqArjCX5b9Lp9O+eA=",
        version = "v1.10.1-0.20190709142728-9a9fa7d4b5f0",
    )
