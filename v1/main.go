package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// ReportAmiAging will contain the bare essential information to summarize the AMI Age of the instances
type ReportAmiAging struct {
	Region             string     `json:"region"`
	InstanceID         string     `json:"instance-id"`
	AmiID              string     `json:"image-id"`
	ImageName          *string    `json:"image-name,omitempty"`
	PlatformDetails    *string    `json:"platform-details,omitempty"`
	InstanceCreateDate *time.Time `json:"instance-create-date"`
	AmiCreateDate      *time.Time `json:"ami-create-date,omitempty"`
	AmiAgeDays         *int       `json:"ami-age-days,omitempty"`
}

// main contains a inelegant level of workflow and detail... but hey this is round v1 😁
func main() {
	startMain := time.Now()

	start := time.Now()
	InitLogger()
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	debug := flag.Bool("debug", false, "sets log level to debug")
	profilename := flag.String("profilename", "", "name of the profile/account to be used, which will also be used in the output file name")
	flag.Parse()
	// Default level for this example is info, unless debug flag is present
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if *debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	// Example Documentation: https://docs.aws.amazon.com/sdk-for-go/v1/developer-guide/ec2-example-create-images.html
	sess, err := session.NewSession(&aws.Config{
		Region: aws.String("eu-west-1"),
	},
	)
	if err != nil {
		log.Err(err)
	}
	log.Info().Str("region", string(*sess.Config.Region)).Msg("initialized new session successfully")

	// Create EC2 service client
	client := ec2.New(sess)

	// Gather all regions that are not opted out
	regions, err := client.DescribeRegions(&ec2.DescribeRegionsInput{
		AllRegions: aws.Bool(true), Filters: []*ec2.Filter{
			{
				Name:   aws.String("opt-in-status"),
				Values: []*string{aws.String("opted-in"), aws.String("opt-in-not-required")},
			},
		},
	},
	)
	if err != nil {
		log.Err(err).Msg("Failed to parse regions")
		os.Exit(1)
	}
	log.Info().Str("APIVersion", string(client.ClientInfo.APIVersion)).Msg("Initialized svc successfully")

	// Get Public Images. This will take a bit.
	start = time.Now()
	req, publicImages := client.DescribeImagesRequest(&ec2.DescribeImagesInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("is-public"),
				Values: []*string{aws.String("true")},
			},
		},
	},
	)
	err = req.Send()
	if err != nil { // resp is now filled
		log.Err(err).Dur("duration", time.Since(start)).Msg("failure to run DescribeImagesRequest")
	}
	log.Info().Int("result_count", len(publicImages.Images)).Dur("duration", time.Since(start)).Msg("\tresults returned for images")

	// Begin to iterate through each region and evaluate the private images for matches. At this time this doesn't also search the public
	Report := []ReportAmiAging{}
	var j []byte
	for _, region := range regions.Regions {
		log.Info().Str("region", *region.RegionName).Msg("--> processing region")
		client := ec2.New(sess, &aws.Config{Region: *&region.RegionName})

		// Get the private images listed in this region
		start = time.Now()
		req, respPrivateImages := client.DescribeImagesRequest(&ec2.DescribeImagesInput{
			Filters: []*ec2.Filter{
				{
					Name:   aws.String("is-public"),
					Values: []*string{aws.String("false")},
				},
			},
		},
		)
		err = req.Send()
		if err != nil { // resp is now filled
			log.Err(err).Dur("duration", time.Since(start)).Msg("failure to run DescribeImagesRequest")
		}
		log.Info().Int("result_count", len(respPrivateImages.Images)).Dur("duration", time.Since(start)).Msg("\tresults returned for images")
		if len(respPrivateImages.Images) == 0 {
			log.Info().Msg("\tno results returned, so exiting further activity in this region")
			continue
		}

		// Get all the EC2 Instances described in this instance as not-terminated
		start = time.Now()
		log.Info().Msg("DescribeInstances")
		respInstances, err := client.DescribeInstances(&ec2.DescribeInstancesInput{
			Filters: []*ec2.Filter{
				{
					Name:   aws.String("instance-state-name"),
					Values: []*string{aws.String("pending"), aws.String("running"), aws.String("shutting-down"), aws.String("stopping"), aws.String("stopped")},
				},
			},
		})
		if err != nil {
			log.Err(err).Msg("\tfailed to run DescribeInstanceStatus")
		}
		log.Info().Int("result_count", len(respInstances.Reservations)).Dur("duration", time.Since(start)).Msg("\tresults returned for ec2instances")
		if len(respInstances.Reservations) == 0 {
			log.Info().Msg("\tno results returned, so exiting further activity in this region")
			continue
		}

		// commented this out, but this could be uncommented and modified to write each region + results to it's own json artifact. I didn't need this.
		// j, err = json.MarshalIndent(respInstances.Reservations, "", "    ")
		// if err != nil {
		// 	log.Err(err).Msg("results marshaled to json")

		// }
		// // log.Info().Msgf("RESULTS OF AMI TO JSON: %s", j)
		// err = ioutil.WriteFile("ec2-info.json", j, 0644) // 0644 sets to overwrite
		// if err != nil {
		// 	log.Err(err).Msg("failure in writing ec2-info.json")
		// }
		// log.Info().Msg("saved results successfully to ec2-info.json")

		// Now for each instance, try to find the matching AMI details frrom both private and public images.
		start = time.Now()
		for idx, res := range respInstances.Reservations {
			log.Info().Str("reservation-id", *res.ReservationId).Int("instance-count", len(res.Instances)).Msg("\ttotal instances found")
			for _, inst := range respInstances.Reservations[idx].Instances {
				log.Debug().Msgf("\t\tinstance-id: %s", *inst.InstanceId)

				var age int
				// var amiCreateDate time.Time

				amiCreateDate, ImageName, platformDetails, err := GetMatchingImage(respPrivateImages.Images, inst.ImageId)
				if err != nil {
					log.Err(err).Msg("failure to find ami")
				}
				if !amiCreateDate.IsZero() {
					age = int(inst.LaunchTime.Sub(amiCreateDate).Hours() / 24.0)
				}

				Report = append(Report, ReportAmiAging{
					Region:             *region.RegionName,
					InstanceID:         *inst.InstanceId,
					AmiID:              *inst.ImageId,
					ImageName:          &ImageName,
					PlatformDetails:    &platformDetails,
					InstanceCreateDate: inst.LaunchTime, // already a pointer... geez aws sdk. could you make it more obtuse?
					AmiCreateDate:      &amiCreateDate,  // pass the pointer
					AmiAgeDays:         &age,
				})
			}
		}

		log.Info().Int("result_count", len(Report)).Dur("duration", time.Since(start)).Msg("\tresults returned for Report by processing struct")
	}

	j, err = json.MarshalIndent(Report, "", "    ")
	if err != nil {
		log.Err(err).Msg("results marshaled to json")
	}
	// log.Info().Msgf("RESULTS OF AMI TO JSON: %s", j)
	filename := strings.Join([]string{*profilename, "report-ami-aging.json"}, "-") //("report-ami-aging.json")
	err = ioutil.WriteFile(filename, j, 0644)                                      // 0644 sets to overwrite
	if err != nil {
		log.Err(err).Msg("failure in writing report-ami-aging.json")
	}
	log.Info().Msg("saved results successfully to report-ami-aging.json")
	log.Info().Dur("duration", time.Since(startMain)).Msg("finished generating results")
}

// GetMatchingImage will search the ami results for a matching id
func GetMatchingImage(imgs []*ec2.Image, search *string) (parsedTime time.Time, imageName string, platformDetails string, err error) {
	layout := time.RFC3339 //"2006-01-02T15:04:05.000Z"
	log.Debug().Msgf("\t\t\tsearching for: %s", *search)
	// Look up the matching image
	for _, i := range imgs {
		log.Trace().Msgf("\t\t\t%s <--> %s", *i.ImageId, *search)
		if strings.ToLower(*i.ImageId) == strings.ToLower(*search) {
			log.Trace().Msgf("\t\t\t %s == %s", *i.ImageId, *search)

			p, err := time.Parse(layout, *i.CreationDate)
			if err != nil {
				log.Err(err).Msg("\t\t\tfailed to parse date from image i.CreationDate")
			}
			log.Debug().Str("i.CreationDate", *i.CreationDate).Str("parsedTime", p.String()).Msg("\t\t\tami-create-date result")
			return p, *i.Name, *i.PlatformDetails, nil
			// break
		}
	}
	return parsedTime, "", "", errors.New("\t\t\tno matching ami found")
}

// InitLogger sets up the logger magic
func InitLogger() {
	output := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
	output.FormatLevel = func(i interface{}) string {
		return strings.ToUpper(fmt.Sprintf("| %-6s|", i))
	}
	output.FormatMessage = func(i interface{}) string {
		return fmt.Sprintf("***%s****", i)
	}
	output.FormatFieldName = func(i interface{}) string {
		return fmt.Sprintf("%s:", i)
	}
	output.FormatFieldValue = func(i interface{}) string {
		return strings.ToUpper(fmt.Sprintf("%s", i))
	}
}
